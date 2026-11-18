# Kubernetes 实战 · Go 运费 API

从一个 Go HTTP 服务开始，练习后端工程师在 Kubernetes（K8s）上最常做的事：打包应用、配置副本、检查服务、发布新版本、回滚和定位故障。示例按整数公斤报价，首公斤 8 元，之后每公斤加 2 元。它也可以用同一个镜像运行一次批处理。

| 模块 | 做完能独立完成什么 |
| --- | --- |
| [1. 上手与首次部署](#1-上手与首次部署) | 把 Go 服务打成容器，用 Deployment 和 Service 运行及访问它 |
| [2. 发布与日常运维](#2-发布与日常运维) | 发布、回滚、更新配置、排查探针故障，运行批处理并把发布接入 CI |
| [3. 服务通信与排障](#3-服务通信与排障) | 让服务通过 DNS 互调，接入 Gateway 和 HTTPS，用指标与日志定位超时 |
| [4. 存储与自动扩缩容](#4-存储与自动扩缩容) | 验证 PVC 跨 Pod 保留数据，观察 HPA 根据 CPU 增减副本 |
| [5. 用 Go 扩展 Kubernetes](#5-用-go-扩展-kubernetes) | 通过 RBAC 调用 API，写一个控制器维护资源并报告状态 |

以 Go 后端开发为目标，可以先做完前三个模块。部署涉及数据或容量管理时做模块 4，需要写平台工具时做模块 5。

## 1. 上手与首次部署

### 从仓库开始

已有本地实验目录时，可直接跳到[进入实验环境](#进入实验环境)。在新机器上，先准备 Docker、kind 和 kubectl，再在 Linux 或 WSL 的 Bash 中克隆并部署：

```bash
git clone https://github.com/XN-Y/K8Study.git
cd K8Study
kind create cluster --name go-lab --config k8s/kind.yaml --image kindest/node:v1.36.4 --wait 120s
docker build --build-arg VERSION=v1 -t go-shipping:v1 .
kind load docker-image go-shipping:v1 --name go-lab
kubectl apply -f k8s/app.yaml
kubectl config set-context kind-go-lab --namespace=go-lab
kubectl rollout status deployment/shipping --timeout=120s
curl -fsS 'http://127.0.0.1:8088/api/shipping?kg=3'
```

后面的本机示例使用 `K8s-Go-Lab` 和 `D:\Project\K8S`。换机器时使用自己的 WSL 实例和克隆目录。

### 进入实验环境

| 项目 | 本机配置 |
| --- | --- |
| 源码 | `D:\Project\K8S`，在 Ubuntu 中挂载为 `/mnt/d/Project/K8S` |
| Go | Windows Go 1.26.10，已加入用户 PATH，重新开 PowerShell 后生效 |
| 容器环境 | WSL 2 实例 `K8s-Go-Lab`，Ubuntu 24.04，Docker Engine 和 Buildx |
| 集群 | kind `go-lab`，Kubernetes 1.36.4，单节点 |
| kubectl context / namespace | `kind-go-lab` / `go-lab` |
| Windows 本机入口 | `http://127.0.0.1:8088` |

在 **PowerShell** 执行，进入该实例的 Bash。下面标为 Bash 的命令都在这里执行，建议逐行运行并观察输出。

```powershell
wsl -d K8s-Go-Lab --cd /mnt/d/Project/K8S
```

```bash
docker version
kind get clusters
kubectl config use-context kind-go-lab
kubectl config set-context --current --namespace=go-lab
kubectl get nodes
kubectl get deploy,rs,pods,svc
curl -fsS http://127.0.0.1:8088/
curl -fsS 'http://127.0.0.1:8088/api/shipping?kg=3'
```

预期节点为 `Ready`，Deployment 为 `2/2`，报价返回 `price_cny: 12`，以及实际 Pod 名和 `version: v1`。在 Windows 浏览器也可以打开 [运费查询](http://127.0.0.1:8088/api/shipping?kg=3)。

先区分三个层次：`kubectl` 通过 kubeconfig 请求 API Server，集群根据对象的 `spec` 采取行动，再把观察到的结果写入 `status`。kind 用 Docker 容器模拟节点，节点内部由 kubelet 和 containerd 启动业务容器。试着执行 `kubectl get deployment shipping -o yaml`，找到 `spec.replicas` 和 `status.readyReplicas`。

### 从 Go 进程到容器

先看 [main.go](main.go)。`/` 用于观察版本和实例，`/api/shipping` 是业务接口，`/livez` 和 `/readyz` 分别用于探活和判断能否接流量。容器收到 SIGTERM 后，Go 会停止接受新连接，并在最多 10 秒内等待进行中的请求。

需要在 Windows 单独验证 Go 时，在 **PowerShell** 启动应用。

```powershell
Set-Location D:\Project\K8S
go run .
```

在另一个 PowerShell 窗口请求。此处的本地 Go 进程使用 8080，镜像里的进程使用它自己的 8080。

```powershell
Invoke-RestMethod 'http://127.0.0.1:8080/api/shipping?kg=3'
```

关闭本地进程后，回到 **Bash**。Dockerfile 的第一阶段构建 Linux 程序，第二阶段只放入编译结果。练习容器有自己的端口映射，可以与集群并行运行。

```bash
docker build --pull=false --build-arg VERSION=v1 -t go-shipping:v1 .
docker run --rm -d --name shipping-docker -p 127.0.0.1:8089:8080 go-shipping:v1
curl -fsS 'http://127.0.0.1:8089/api/shipping?kg=3'
docker logs shipping-docker
docker stop shipping-docker
```

自己试一下：把 `kg` 改为 0 或 21，用 `curl -i` 看 HTTP 400。运行 `docker image ls go-shipping` 查看镜像标签。

### 用 YAML 声明应用状态

```bash
kind load docker-image go-shipping:v1 --name go-lab
kubectl apply -f k8s/app.yaml
kubectl rollout status deployment/shipping --timeout=120s
kubectl get deployment,replicaset,pods,svc -o wide
kubectl get endpointslices -l kubernetes.io/service-name=shipping
```

用 `kubectl get pods -n kube-system` 看集群组件。API Server 接受请求，etcd 保存集群状态，控制器比较实际状态与期望状态，调度器为新 Pod 分配节点，节点上的 kubelet 负责启动容器并运行探针。CoreDNS 负责集群内服务名解析。

对象关系可以用下面的命令逐个查看。

```bash
kubectl describe deployment shipping
kubectl describe service shipping
```

打开 [k8s/app.yaml](k8s/app.yaml)，对照下面的关系逐项查找字段。

- **Deployment** 描述镜像、目标副本数和更新策略。它维护 ReplicaSet，ReplicaSet 补齐缺失的 Pod。
- **Service** 根据 `app: shipping` 选择 Pod，集群内其他服务可以访问 `http://shipping.go-lab.svc.cluster.local`。EndpointSlice 能看到后端 IP 与可用状态。
- **ConfigMap** 提供 `GREETING`。Pod 名由 Downward API 从 Pod 元数据注入进程。
- **requests/limits** 中的 `25m` 是 0.025 个 CPU。调度时以 requests 为依据。CPU 达到 limit 会被限速，超出内存 limit 的进程可能被 OOM kill。

从 Windows 发请求时，链路如下。

```text
Windows 127.0.0.1:8088
    → WSL Docker 发布的端口
    → kind 节点的 NodePort 30080
    → Service shipping 的可用后端
    → Pod 内的 Go 进程 :8080
```

集群内可以直接用 DNS 名访问同一个 Service。运行一个短命 BusyBox Pod 检查服务发现和跨 Pod 网络，命令结束后从日志读出响应。

```bash
kind load docker-image busybox:1.37.0 --name go-lab
kubectl run net-check --image=busybox:1.37.0 --image-pull-policy=IfNotPresent --restart=Never --command -- wget -qO- 'http://shipping.go-lab.svc.cluster.local/api/shipping?kg=3'
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded pod/net-check --timeout=45s
kubectl logs pod/net-check
kubectl delete pod net-check
```

在 Bash 多发几次请求。每次连接可能落到不同的 Pod，分配顺序可以变化。

```bash
for i in {1..12}; do curl -fsS http://127.0.0.1:8088/; done
```

再试一种临时访问方式。在一个 Bash 终端运行 `kubectl port-forward svc/shipping 8090:80`，另一个终端请求 `curl -fsS http://127.0.0.1:8090/`。port-forward 会为这次会话选择一个后端 Pod。按 Ctrl+C 停止。

## 2. 发布与日常运维

### 扩缩容与 Pod 自愈

在一个 Bash 终端执行 `kubectl get pods -l app=shipping -w` 持续观察，在另一个终端执行：

```bash
kubectl scale deployment/shipping --replicas=3
kubectl rollout status deployment/shipping --timeout=120s
pod=$(kubectl get pods -l app=shipping -o jsonpath='{.items[0].metadata.name}')
kubectl delete pod "$pod"
```

记录旧 Pod 和新 Pod 的名字。旧 Pod 删除后会出现新 Pod，副本数最终回到 3。再恢复练习的初始规模：

```bash
kubectl scale deployment/shipping --replicas=2
kubectl rollout status deployment/shipping --timeout=120s
```

这里的三个副本都跑在同一个节点上。生产中要承受节点故障，还需多个节点、合适的调度策略及足够的容量。

### 发布 v2 并回滚

本节用构建参数改变 API 返回的版本号，方便观察发布过程。真实功能上线时，可以先改 `main.go` 的业务逻辑，再走同样的流程。

```bash
docker build --pull=false --build-arg VERSION=v2 -t go-shipping:v2 .
kind load docker-image go-shipping:v2 --name go-lab
kubectl set image deployment/shipping api=go-shipping:v2
kubectl get rs
kubectl rollout status deployment/shipping --timeout=120s
curl -fsS 'http://127.0.0.1:8088/api/shipping?kg=3'
kubectl rollout history deployment/shipping
kubectl rollout undo deployment/shipping
kubectl rollout status deployment/shipping --timeout=120s
curl -fsS 'http://127.0.0.1:8088/api/shipping?kg=3'
```

两次请求的版本应依次为 `v2` 和 `v1`。`maxSurge: 1` 允许发布时临时多一个 Pod，`maxUnavailable: 0` 要求发布过程中维持期望的可用副本数。此次用 `set image` 和 `rollout undo` 直接操作集群，`k8s/app.yaml` 仍声明 v1。如果看到 `last-applied-configuration` 警告，它是在提醒你命令式回滚不会同步声明式配置。生产中通常由 CI 构建并推送镜像到仓库，再修改版本库里的部署清单并发布。

### 修改配置并排查发布故障

先修改问候语。ConfigMap 通过环境变量读取，所以修改配置后需要重建 Pod。

```bash
kubectl patch configmap shipping-config --type=merge -p '{"data":{"GREETING":"ConfigMap updated"}}'
curl -fsS http://127.0.0.1:8088/
kubectl rollout restart deployment/shipping
kubectl rollout status deployment/shipping --timeout=120s
curl -fsS http://127.0.0.1:8088/
```

然后故意把新 Pod 的 readiness 路径改成一个不存在的地址。补丁中的 `api` 对应 Deployment 中的容器名。

```bash
kubectl patch deployment shipping --type=strategic -p '{"spec":{"template":{"spec":{"containers":[{"name":"api","readinessProbe":{"httpGet":{"path":"/missing"}}}]}}}}'
kubectl rollout status deployment/shipping --timeout=15s
kubectl get pods -l app=shipping
kubectl get events --sort-by=.lastTimestamp
kubectl describe pods -l app=shipping
kubectl logs -l app=shipping --prefix --tail=20
curl -fsS http://127.0.0.1:8088/
```

预期发布观察超时，新 Pod 显示 `0/1 Running`，事件里可见 readiness HTTP 404。已有的两个 Ready Pod 仍会响应请求。三种探针各自回答一个问题：

- **startup**：应用是否完成启动？成功前，Kubernetes 暂缓运行另外两个探针。
- **readiness**：这个实例是否可以接流量？失败后，通常从 Service 的可用后端移除。
- **liveness**：这个进程能否继续运行？连续失败达到阈值时，kubelet 重启容器。

恢复发布并还原配置。重启确保环境变量也还原。

```bash
kubectl rollout undo deployment/shipping
kubectl rollout status deployment/shipping --timeout=120s
kubectl apply -f k8s/app.yaml
kubectl rollout restart deployment/shipping
kubectl rollout status deployment/shipping --timeout=120s
curl -fsS http://127.0.0.1:8088/
```

日常排查时，先用 `kubectl get pods -o wide` 看状态，再用 `describe` 看事件，最后根据原因选命令。

| 现象 | 先看哪里 |
| --- | --- |
| `Pending`，迟迟没有节点 | `describe pod` 中的 `FailedScheduling`，检查 requests、节点和卷 |
| `ImagePullBackOff` | 核对镜像名与标签、仓库权限、本地镜像是否导入正确集群 |
| `CrashLoopBackOff` | `kubectl logs POD --previous` 和 `describe pod POD` 的退出原因 |
| 进程在跑，但访问不通 | 检查 readiness、Service selector、EndpointSlice 和 targetPort |
| 更新卡住 | `kubectl get rs` 区分新旧版本，再查看新 Pod 的事件 |

### 用同一个 Go 镜像跑批处理

`/app report` 生成一条模拟报表日志后退出。先从 CronJob 模板手动创建一个 Job，立即验证行为。

```bash
kubectl apply -f k8s/cronjob.yaml
job="shipping-report-$(date +%s)"
kubectl create job --from=cronjob/shipping-report "$job"
kubectl wait --for=condition=complete "job/$job" --timeout=90s
kubectl logs "job/$job"
kubectl get cronjob,jobs
```

预期日志包含 `demo shipping report generated`，Job 完成后由 TTL 控制器清理。CronJob 默认暂停。要看自动触发，可以解除暂停并观察下一个 5 分钟刻度。如果暂停期间已有错过的调度，恢复时也可能立即创建一次 Job。观察完再暂停。

```bash
kubectl patch cronjob shipping-report --type=merge -p '{"spec":{"suspend":false}}'
kubectl get jobs -w
# Ctrl+C 结束观察，然后暂停新的调度
kubectl patch cronjob shipping-report --type=merge -p '{"spec":{"suspend":true}}'
```

实际报表、对账和重试任务可能重复执行。写 Go 任务时，可以用报表日期、订单号或任务 ID 设计幂等。

### 一键发布与 CI

[本地发布脚本](scripts/release-shipping.sh) 串起手动发布的步骤：先运行主 Go 模块的测试，再构建镜像、导入 kind、更新 Deployment 并等待滚动发布。脚本固定使用实验的 context 和命名空间，每次发布使用新的镜像标签。

在实验 **Bash** 执行：

```bash
release="lab-$(date +%Y%m%d-%H%M%S)"
bash scripts/release-shipping.sh "$release"
curl -fsS 'http://127.0.0.1:8088/api/shipping?kg=3'
kubectl rollout history deployment/shipping
```

接口的 `version` 应等于刚才生成的标签。再用同一个标签运行一次，脚本会在更新 Deployment 之前拒绝。测试、构建或导入失败也会在更新前停止。发布卡住时，可以用[发布故障练习](#修改配置并排查发布故障)的事件和探针命令定位原因，再执行回滚。

```bash
kubectl rollout undo deployment/shipping
kubectl rollout status deployment/shipping --timeout=120s
curl -fsS 'http://127.0.0.1:8088/api/shipping?kg=3'
```

接着做 [GitHub Actions 和镜像仓库实战](labs/ci-release.md)。用 Git 提交号标记镜像，由流水线运行测试和构建并发布到 GHCR。使用本地镜像仓库也可以练习按镜像摘要部署和回滚。

## 3. 服务通信与排障

这三项练习以订单结算为例，从服务间调用走到外部入口和故障定位。

1. [双服务交付](labs/two-services.md)。让 Go 结算服务通过 Service DNS 调用运费服务，再用同一个 Gateway 按路径分发两种请求。故意写错下游地址，定位结算返回 502 的原因。
2. [HTTPS 入口](labs/https.md)。为实验域名签发证书，通过 TLS Secret 配置 Gateway，在 Windows 验证证书、域名和路由。
3. [应用可观测性](labs/observability.md)。用 Prometheus 查询请求量、错误率和延迟，用总耗时与下游耗时的日志定位一次 504。

## 4. 存储与自动扩缩容

这两项练习各用一个独立工作负载。都在 `go-lab` 命名空间运行。

- [PVC 持久化](labs/README.md#pvc-持久化)。用 [PVC 和 Deployment](k8s/storage.yaml) 写入一份报表，删除 Pod 后检查替代 Pod 能否读取它。
- [CPU 自动扩缩容](labs/README.md#cpu-指标驱动自动扩缩容)。用 [Metrics Server](k8s/metrics-server/kustomization.yaml) 和 [HPA](k8s/hpa.yaml) 观察负载升降时的副本变化。

## 5. 用 Go 扩展 Kubernetes

应用团队日常主要写 Go 业务和部署配置。下面两个练习适合需要从 Go 查询 Kubernetes API、编排资源或编写平台工具的开发者。

1. [用 Go 读取 Kubernetes API](labs/README.md#用-go-读取-kubernetes-api)。用 [client-go 源码](labs/pod-reader/main.go) 和 [RBAC 配置](k8s/pod-reader.yaml)，让专用身份查询命名空间内的 Pod，再验证跨命名空间访问被拒绝。
2. [Go 控制器实战](labs/controller.md)。用 `ShippingPreview` 声明版本和副本数量，观察控制器创建、修正和重建 Deployment，再用 `status.conditions` 检查变更是否就绪。

## 附录

### 检查部署

在实验 Bash 运行[部署检查脚本](scripts/check-lab.sh)。默认验证节点、运费 Deployment、Service 后端和 `8088` 接口。完成了双服务、HTTPS、监控和控制器实验后，可使用完整检查。

```bash
bash scripts/check-lab.sh
bash scripts/check-lab.sh --full
```

完整检查还会验证结算调用、Gateway 和路由条件、TLS 证书有效期、控制器与指标采集。脚本读取集群资源并发送测试请求。从 Windows 验证 TLS 握手及域名匹配时，按 [HTTPS 入口](labs/https.md) 的步骤启动转发。

检查运行中资源与清单的差异时，可用 `kubectl diff -f k8s/app.yaml -f k8s/checkout.yaml`。如果刚做过手动扩缩容、配置补丁或镜像切换，先查看差异是否来自练习，再决定是否重新应用清单。

### 停止、恢复与重建

初次部署后有一个后台 WSL 会话保持实验在线，方便直接从 Windows 浏览器访问。临时释放资源时，在 **PowerShell** 停止实验 WSL 实例，这也会结束该后台会话。

```powershell
wsl --terminate K8s-Go-Lab
```

下次按[进入实验环境](#进入实验环境)打开 Bash，练习期间保留这个窗口。没有活动终端或后台会话时，WSL 可能自动停止实例。检查 Docker 和节点，如果节点容器处于停止状态：

```bash
docker start go-lab-control-plane
kubectl wait --for=condition=Ready node/go-lab-control-plane --timeout=120s
kubectl rollout status deployment/shipping --timeout=120s
```

要从头练习时，可以只清理实验应用所在的命名空间。该操作也会删除其中的配置和任务。然后按[用 YAML 声明应用状态](#用-yaml-声明应用状态)重新部署。

```bash
kubectl --context kind-go-lab delete namespace go-lab
```

如果专门练习创建集群，下面的删除命令会移除这个 kind 集群及集群内数据。Docker 中已有的镜像可以继续使用。

```bash
kind delete cluster --name go-lab
kind create cluster --name go-lab --config k8s/kind.yaml --image kindest/node:v1.36.4 --wait 120s
kubectl config set-context --current --namespace=go-lab
kind load docker-image go-shipping:v1 --name go-lab
kubectl apply -f k8s/app.yaml
kubectl rollout status deployment/shipping --timeout=120s
```

### 常见环境问题

- WSL 可能提示 Windows 的 localhost 代理未镜像到 NAT 实例。这台机器的初始化镜像通过 Windows 下载并导入，后续本地练习可以复用缓存。用 `docker image ls` 查看。需要新的远程镜像时，可通过能访问仓库的环境下载 Docker 格式归档，再用 `docker load -i` 导入。
- Windows 访问 8088 如遇代理干扰，可以用 `curl.exe --noproxy "*" "http://127.0.0.1:8088/"` 验证。还可以先进入实验 Bash，从 WSL 内发请求，区分 WSL 到 Windows 的转发和集群内问题。
- Docker 访问报权限错误时，确认当前终端位于 `K8s-Go-Lab`，并检查 `id` 是否有 `docker` 组。若提示守护进程未启动，可在 PowerShell 执行 `wsl -d K8s-Go-Lab -u root -- systemctl start docker` 后重试。

### 后续专题

| 专题 | 动手做什么 | 验收标准 |
| --- | --- | --- |
| 生产证书 | 给实际业务域名配置 DNS 和可信 CA 证书，并接入自动续期 | 用客户端默认信任链访问域名，证书轮换后仍能正常握手 |
| 告警和链路追踪 | 为结算指标配置告警，再给双服务调用加 OpenTelemetry trace | 收到告警后可以在一次请求的 trace 中定位慢调用 |
| GitOps 发布 | 将 CI 产出的镜像摘要写入环境清单，通过合并请求审批，再由部署控制器同步 | 从 Git 历史查到哪个提交向哪个环境发布了哪份镜像 |
| 控制器进阶 | 为预览控制器加集成测试，再尝试 leader election 和可控重试 | 能验证控制器重启、API 冲突和多个实例竞争时的行为 |
| StatefulSet | 给每个有状态副本声明单独的 PVC | 重建 Pod 后确认其稳定名称与卷身份，理解备份与持久化的区别 |

### 官方资料

- [kind 快速入门和本地镜像加载](https://kind.sigs.k8s.io/docs/user/quick-start/)
- [Deployment 和滚动发布](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/)
- [Service 和 EndpointSlice](https://kubernetes.io/docs/concepts/services-networking/service/)
- [容器探针](https://kubernetes.io/docs/concepts/workloads/pods/probes/)
- [ConfigMap](https://kubernetes.io/docs/concepts/configuration/configmap/)
- [CronJob](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/)
- [HPA](https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/)
- [Gateway API](https://gateway-api.sigs.k8s.io/)
- [client-go](https://github.com/kubernetes/client-go)
