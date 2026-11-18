# 存储、弹性与 Go API 实验

按[进入实验环境](../README.md#进入实验环境)打开 Bash，确认当前 context 为 `kind-go-lab`，命名空间为 `go-lab`，并用 `kubectl get deployment shipping` 检查运费服务的副本是否就绪。

## 用 Go 读取 Kubernetes API

平台里的巡检工具、发布工具和控制器经常需要读取 Kubernetes 对象。这里实现一个只列出运费服务 Pod 的 Go 程序。先打开 [main.go](pod-reader/main.go)，找到 `rest.InClusterConfig()`、`client.Pods(...).List(...)` 和请求超时。

在一个新开的 **PowerShell** 窗口编译 Linux 二进制。编译需要下载 `go.mod` 中锁定的依赖。环境变量只影响这个窗口，构建完可关闭它。

```powershell
Set-Location D:\Project\K8S\labs\pod-reader
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -trimpath -o pod-reader-linux-amd64 .
```

回到实验 **Bash**。如果只想先看结果，本机已有构建好的 `go-pod-reader:v1` 镜像，可以从 `kind load` 开始。修改代码后重新构建时建议为新镜像使用新标签，并同步修改 YAML 里的 `image`。

```bash
docker build --pull=false -t go-pod-reader:v1 labs/pod-reader
kind load docker-image go-pod-reader:v1 --name go-lab
kubectl apply -f k8s/pod-reader.yaml
kubectl wait --for=condition=complete job/pod-reader --timeout=90s
kubectl logs job/pod-reader
```

预期看到两个 Pod 的名字、阶段以及 `Found 2 shipping pods in go-lab`。认证和授权分别发生在这里：

- **认证**：Pod 使用专用 `ServiceAccount`。client-go 从 Pod 环境和挂载文件读取 API 地址、证书和凭据。
- **授权**：`Role` 允许 `list pods`，`RoleBinding` 把它授给 `pod-reader`，范围为 `go-lab`。Go 的 label selector 只过滤查询结果，RBAC 仍授权列出该命名空间内的全部 Pod。

用 `kubectl auth can-i` 分别验证同命名空间的读取、删除和跨命名空间读取。预期依次为 `yes`、`no`、`no`。后两条的非零退出码表示授权被拒绝。

```bash
identity=system:serviceaccount:go-lab:pod-reader
kubectl auth can-i list pods -n go-lab --as="$identity"
kubectl auth can-i delete pods -n go-lab --as="$identity"
kubectl auth can-i list pods -n kube-system --as="$identity"
```

再让同一个 Go 程序真实请求 `kube-system`。Pod 应以 `Failed` 结束，日志中出现 `forbidden`。

```bash
kubectl run pod-reader-denied --image=go-pod-reader:v1 --image-pull-policy=IfNotPresent --restart=Never \
  --overrides='{"spec":{"serviceAccountName":"pod-reader"}}' -- -namespace=kube-system
kubectl wait --for=jsonpath='{.status.phase}'=Failed pod/pod-reader-denied --timeout=60s
kubectl logs pod/pod-reader-denied
kubectl delete pod pod-reader-denied
kubectl delete -f k8s/pod-reader.yaml
```

思考题：如果增加一个读取单个 Pod 的功能，除了修改 Go 方法，还需要给 Role 增加哪个 verb？

## PVC 持久化

典型场景是内部工具的文件或数据库数据目录。这个练习用 BusyBox 写入一份模拟报表。Pod 通过 PVC 请求并挂载卷，StorageClass 决定怎样供应与 PVC 绑定的 PV。

```bash
kind load docker-image busybox:1.37.0 --name go-lab
kubectl get storageclass
kubectl apply -f k8s/storage.yaml
kubectl rollout status deployment/storage-demo --timeout=120s
kubectl get pvc lab-data
kubectl get pv

before=$(kubectl get pod -l app=storage-demo -o jsonpath='{.items[0].metadata.name}')
kubectl exec "$before" -- sh -c 'printf "%s\n" "sample shipping report" > /data/report.txt'
kubectl delete pod "$before"
kubectl rollout status deployment/storage-demo --timeout=120s
after=$(kubectl get pod -l app=storage-demo -o jsonpath='{.items[0].metadata.name}')
printf 'Old pod: %s\nNew pod: %s\n' "$before" "$after"
kubectl exec "$after" -- cat /data/report.txt
```

验收时检查 PVC 为 `Bound`、新旧 Pod 名不同，并且能读到 `sample shipping report`。kind 的 `standard` StorageClass 使用本地存储和 `WaitForFirstConsumer`，在出现需要挂载此 PVC 的 Pod 时才触发绑定和供应。卷的数据位于节点的本地存储中，删除 kind 节点或 PVC 都可能丢失该数据。生产中用卷之前还要考虑节点故障、存储后端及备份恢复。

练完后可以清理。下面先移除 Deployment，最后删除 PVC，结束这个实验中的数据保留。

```bash
kubectl delete deployment storage-demo
kubectl delete pvc lab-data
```

## CPU 指标驱动自动扩缩容

这里用单独的 `cpu-demo` 工作负载演示 HPA。每个 Pod 请求 `50m` CPU，上限 `100m`。HPA 的目标是平均使用量达到请求量的 50%，也就是每 Pod 平均 `25m`。副本数在 1 到 3 之间。

### 检查指标是否可用

当前实验集群已安装 Metrics Server v0.9.1。重建集群后，可以从已缓存的镜像和本地清单重新安装。

```bash
kind load docker-image registry.k8s.io/metrics-server/metrics-server:v0.9.1 --name go-lab
kubectl apply -k k8s/metrics-server
kubectl rollout status deployment/metrics-server -n kube-system --timeout=120s
kubectl wait --for=condition=Available apiservice/v1beta1.metrics.k8s.io --timeout=120s
kubectl top nodes
kubectl top pods -l app=shipping
```

[Kustomize 配置](../k8s/metrics-server/kustomization.yaml) 在上游清单上追加了 `--kubelet-insecure-tls`，用来适应此本地 kind 集群的 kubelet 证书。生产中应为 kubelet 配置可验证的服务端证书。指标刚启动时可能需要等几个采样周期。若持续没有数据，可用 `kubectl logs -n kube-system deployment/metrics-server` 和 `kubectl describe apiservice v1beta1.metrics.k8s.io` 定位问题。

### 制造短时 CPU 负载并观察

在一个实验 Bash 终端执行：

```bash
kind load docker-image busybox:1.37.0 --name go-lab
kubectl apply -f k8s/hpa.yaml
kubectl rollout status deployment/cpu-demo --timeout=120s
kubectl get hpa cpu-demo -w
```

等 `TARGETS` 显示数值后，在另一个实验 Bash 终端中运行下面的命令。它在一个 Pod 内启动可自动停止的 CPU 循环，最多运行 90 秒。

```bash
kubectl exec deployment/cpu-demo -- sh -c 'timeout 90 yes >/dev/null'
```

观察第一个窗口，预期副本从 1 增加。90 秒后出现退出码 143 表示 `timeout` 已终止负载进程。继续观察，通常在几个采样周期和 30 秒缩容稳定窗口过后回到 1。指标变化有延迟，副本数以实际输出为准。

```bash
# 在观察窗口按 Ctrl+C 后运行
kubectl describe hpa cpu-demo
kubectl top pods -l app=cpu-demo
kubectl delete -f k8s/hpa.yaml
```

验收时在 `describe` 中找 `SuccessfulRescale` 事件，并说清楚为什么使用量相同、CPU request 不同时，HPA 可能做出不同的决定。实际 Go 服务还要结合延迟、错误率和下游容量判断扩容效果。

## 综合练习

先记录当前镜像标签和发布 revision。把运费首公斤价格改为 10 元，给新镜像一个未用过的标签，再发布到现有 Service 后面。用 `kg=3` 验证价格从 12 变为 14，用 Pod 巡检程序确认新副本正常运行。接着让一次更新的 readiness 探针返回 404，找出未就绪的 Pod，确认 Service 仍返回 14 元。恢复探针后，按记录的 revision 回滚，再验证价格回到 12 元。

复盘时保留三项证据：发布前后的接口响应、探针失败时的事件，以及恢复后 Deployment 的就绪副本数。

下一步是[双服务交付实战](two-services.md)。在现有运费 API 前增加一个 Go 结算服务，用 Service DNS 串起依赖，再用 Gateway API 给两个服务提供统一入口。

## 官方资料

- [client-go 集群内认证示例](https://github.com/kubernetes/client-go/tree/master/examples/in-cluster-client-configuration)
- [Kubernetes RBAC](https://kubernetes.io/docs/reference/access-authn-authz/rbac/)
- [持久卷实验](https://kubernetes.io/docs/tutorials/configuration/configure-persistent-volume-storage/)
- [Metrics Server v0.9.1](https://github.com/kubernetes-sigs/metrics-server/releases/tag/v0.9.1)
- [Kubernetes HPA](https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/)
