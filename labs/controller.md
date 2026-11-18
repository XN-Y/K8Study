# Go 控制器实战

[用 Go 读取 Kubernetes API](README.md#用-go-读取-kubernetes-api)练习了查询 Pod。这里用 controller-runtime 写一个小控制器，自动维护运费服务的预览环境。这类模式也用于按配置创建环境、编排任务和管理数据库实例。

```text
ShippingPreview 的 spec → Go Reconcile → Deployment → ReplicaSet → Pod
                             ↑                  │
                             └─ 子对象的变化事件 ┘
```

## 认识资源和控制器

| 文件 | 负责什么 |
| --- | --- |
| [preview-crd.yaml](../k8s/preview-crd.yaml) | 注册 `ShippingPreview`，限制版本为 v1/v2、副本数为 0–3 |
| [main.go](preview-controller/main.go)、[status.go](preview-controller/status.go) | 监听预览资源与其拥有的 Deployment，核对版本和副本数并报告就绪状态 |
| [preview-controller.yaml](../k8s/preview-controller.yaml) | 运行控制器，在 `go-lab` 授予读预览资源和维护 Deployment 的权限 |
| [shipping-preview.yaml](../k8s/shipping-preview.yaml) | 声明一个名为 `shipping-lab` 的预览，初始为 v1、一个副本 |

源码里 `For(newPreview())` 监听预览资源，`Owns(&appsv1.Deployment{})` 把子 Deployment 的事件映射回父资源。每次 reconcile 重新读取当前 `spec`。`CreateOrUpdate` 比较当前和目标，只在有变化时更新；更新携带 `resourceVersion`，遇到并发修改时 API 会拒绝旧版本，控制器再协调。子 Deployment 带有 `ownerReferences`，用来关联事件，并在删除父资源时由 Kubernetes 回收。

## 编译和部署

先完成[首次部署](../README.md#用-yaml-声明应用状态)。如果缺少控制器镜像，在一个新开的 **PowerShell** 编译 Linux 程序，再回到实验 Bash 构建镜像。

```powershell
Set-Location D:\Project\K8S\labs\preview-controller
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -trimpath -ldflags '-s -w' -o preview-controller-linux-amd64 .
```

使用 Linux 或 WSL 中的 Go 时，也可以在仓库根目录编译：

```bash
(cd labs/preview-controller && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o preview-controller-linux-amd64 .)
```

```bash
# 源码改动后构建新镜像时，选新标签并同步修改 k8s/preview-controller.yaml
docker build --pull=false -t go-preview-controller:v2 labs/preview-controller
```

预览实验需要运费 v1 和 v2。首次部署已构建 v1，如果还没做过滚动发布练习，从仓库根目录构建 v2。

```bash
docker image inspect go-shipping:v2 >/dev/null 2>&1 ||
  docker build --pull=false --build-arg VERSION=v2 -t go-shipping:v2 .
```

已有控制器和运费镜像后，从项目根目录应用清单：

```bash
kind load docker-image go-preview-controller:v2 go-shipping:v1 go-shipping:v2 --name go-lab
kubectl apply -f k8s/preview-crd.yaml
kubectl wait --for=condition=Established crd/shippingpreviews.lab.example.com --timeout=60s
kubectl apply -f k8s/preview-controller.yaml
kubectl rollout status deployment/preview-controller --timeout=120s
kubectl apply -f k8s/shipping-preview.yaml
kubectl wait --for=create deployment/preview-shipping-lab --timeout=60s
kubectl wait --for=condition=Ready shippingpreview/shipping-lab --timeout=120s
kubectl get shippingpreview shipping-lab
kubectl get deployment preview-shipping-lab -o jsonpath='{.metadata.ownerReferences[0].kind}{" / "}{.metadata.ownerReferences[0].name}{"\n"}'
```

预期是一个 v1 预览副本，owner 显示 `ShippingPreview / shipping-lab`。预览 Pod 使用独立标签。原来的 shipping Service 通过 `app: shipping` 选择后端。

## 改目标、制造漂移、删除子对象

修改自定义资源的版本和副本数，观察 Deployment 随之更新：

```bash
kubectl patch shippingpreview shipping-lab --type=merge -p '{"spec":{"version":"v2","replicas":2}}'
kubectl wait --for=jsonpath='{.spec.replicas}'=2 deployment/preview-shipping-lab --timeout=60s
kubectl wait --for=condition=Ready shippingpreview/shipping-lab --timeout=120s
kubectl get deployment preview-shipping-lab -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
```

预期显示 `go-shipping:v2`。还可以用 `kubectl annotate shippingpreview shipping-lab lab.example.com/check=idempotence --overwrite` 触发一次更新事件，前后检查 Deployment 的 `.metadata.generation`。预览目标没有变化，两个 generation 应相同。

手工把子 Deployment 缩到一个副本。控制器收到子对象变更，应该将它修正为父资源声明的两个副本。

```bash
kubectl scale deployment/preview-shipping-lab --replicas=1
kubectl wait --for=jsonpath='{.spec.replicas}'=2 deployment/preview-shipping-lab --timeout=60s
kubectl logs deployment/preview-controller --tail=12
```

最后删除 Deployment，比较重建前后的 UID：

```bash
before=$(kubectl get deployment preview-shipping-lab -o jsonpath='{.metadata.uid}')
kubectl delete deployment preview-shipping-lab
kubectl wait --for=create deployment/preview-shipping-lab --timeout=60s
kubectl rollout status deployment/preview-shipping-lab --timeout=120s
after=$(kubectl get deployment preview-shipping-lab -o jsonpath='{.metadata.uid}')
printf 'Before %s\nAfter  %s\n' "$before" "$after"
```

名称相同且 UID 不同，说明它是重新创建的对象。若想再验证 API 校验，可以提交副本数为 `4` 的补丁，预期 API 会拒绝它，控制器继续按照最后一次有效声明工作。

## 清理重建练习的资源

删除父资源，等待其拥有的 Deployment 被垃圾回收：

```bash
kubectl delete -f k8s/shipping-preview.yaml
kubectl wait --for=delete deployment/preview-shipping-lab --timeout=60s
kubectl get deployment shipping checkout preview-controller
```

完成后想一想：如果运费 Pod 自己崩溃，哪个控制器负责补 Pod？如果整个预览 Deployment 消失，谁负责把它建回来？

## 用 status 判断本次变更是否就绪

`spec` 记录你的目标，`status` 记录控制器观察到的结果。CRD 启用了 `/status` 子资源，控制器通过单独的 RBAC 权限更新它。可以重新应用样例并检查状态：

```bash
kubectl apply -f k8s/shipping-preview.yaml
kubectl wait --for=condition=Ready shippingpreview/shipping-lab --timeout=120s
kubectl get shippingpreview shipping-lab -o yaml
```

`status.readyReplicas` 是 Deployment 报告的就绪副本数。判断 `Ready=True` 时，控制器还会检查 Deployment 已观察到当前版本，并且满足目标数量的副本已更新且可用。条件中的 `observedGeneration` 应等于预览资源的 `metadata.generation`，这样就能确认读到的是这次变更的结果。如果目标副本数是 0，`Ready=True` 表示已经完成缩容。

| Ready 的 reason | 怎样解读 |
| --- | --- |
| `DeploymentProgressing` | Deployment 正在处理当前版本，等待更新后的副本可用 |
| `DeploymentAvailable` | 本次变更需要的副本已经可用 |
| `DeploymentRolloutFailed` | Deployment 报告滚动更新失败，可用 `describe deployment` 继续追查 |
| `OwnershipConflict` | 同名 Deployment 已存在，控制器等待冲突解除 |
| `ReconcileFailed` | 更新资源时遇到错误，查条件的 `message` 和控制器日志 |

### 制造一次同名冲突

先创建一个不属于控制器的 Deployment，再申请同名预览。这个临时 Deployment 的副本数为 0。用 `status.conditions` 观察冲突原因：

```bash
kubectl create deployment preview-status-conflict --image=go-shipping:v1 --replicas=0
kubectl apply -f - <<'YAML'
apiVersion: lab.example.com/v1alpha1
kind: ShippingPreview
metadata:
  name: status-conflict
  namespace: go-lab
spec:
  version: v2
  replicas: 1
YAML
kubectl wait --for=jsonpath='{.status.conditions[?(@.type=="Ready")].reason}'=OwnershipConflict \
  shippingpreview/status-conflict --timeout=60s
kubectl describe shippingpreview status-conflict
```

确认是刚创建的实验对象后，删除占用该名称的 Deployment。控制器会重试，创建由预览资源管理的 Deployment，并更新为 `Ready=True`。

```bash
kubectl delete deployment preview-status-conflict
kubectl wait --for=condition=Ready shippingpreview/status-conflict --timeout=60s
kubectl get shippingpreview status-conflict
kubectl delete shippingpreview status-conflict shipping-lab
kubectl wait --for=delete deployment/preview-status-conflict deployment/preview-shipping-lab --timeout=60s
```

官方资料：[controller-runtime 版本兼容](https://github.com/kubernetes-sigs/controller-runtime#compatibility)、[CRD 校验与默认值](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/)、[Kubernetes 垃圾回收](https://kubernetes.io/docs/concepts/architecture/garbage-collection/)。
