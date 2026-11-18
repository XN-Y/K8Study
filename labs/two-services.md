# 双服务交付实战

把练习扩展为一个小型电商后端。运费服务负责定价，结算服务负责把商品金额和运费相加。两个服务有各自的镜像、Deployment 和 Service，入口按请求路径路由。

```text
Windows 127.0.0.1:8091
  → kubectl port-forward → Envoy Gateway 的代理
    ├─ /api/shipping → shipping Service → 运费 Pod
    └─ /api/checkout → checkout Service → 结算 Pod
                                            └─ 通过集群 DNS 调用 shipping Service
```

按[进入实验环境](../README.md#进入实验环境)打开 Bash。下面除明确标注的 PowerShell 命令外，都在 `/mnt/d/Project/K8S` 执行。

## 用 Go 实现服务间调用

打开 [结算服务](../cmd/checkout/main.go) 和 [部署清单](../k8s/checkout.yaml)，找到部署注入的 `SHIPPING_API_URL`。它指向 `http://shipping.go-lab.svc.cluster.local/api/shipping`。这个名字包含 Service 名、命名空间和集群 DNS 后缀。扩容或替换运费 Pod 时，调用方仍使用同一个名称。

```bash
docker build --pull=false -t go-checkout:v2 -f cmd/checkout/Dockerfile .
kind load docker-image go-checkout:v2 --name go-lab
kubectl apply -f k8s/checkout.yaml
kubectl rollout status deployment/checkout --timeout=120s
kubectl get deployment/shipping deployment/checkout service/shipping service/checkout
```

从集群内部请求结算 Service，核对返回的运费 Pod 和合计金额。

```bash
bash scripts/load-lab-images.sh busybox:1.37.0
kubectl run checkout-check --image=busybox:1.37.0 --image-pull-policy=IfNotPresent --restart=Never \
  --command -- wget -qO- 'http://checkout.go-lab.svc.cluster.local/api/checkout?subtotal_cny=100&kg=3'
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded pod/checkout-check --timeout=60s
kubectl logs pod/checkout-check
kubectl delete pod checkout-check
```

预期 `subtotal_cny` 为 100，`shipping.price_cny` 为 12，`total_cny` 为 112。Go 客户端复用一个 `http.Client`，为下游请求设 2 秒总超时，并传递进来的请求上下文。输入有误时结算服务返回 400，下游调用失败返回 502，下游超时返回 504。

在 Windows 的项目目录运行 `go test ./cmd/checkout`，可以独立验证运费报价解析、金额计算、错误响应和超时映射。

## 用 Gateway API 接入两个服务

如果集群还没有 Envoy Gateway v1.10.0，先执行[安装控制器](#安装或恢复-envoy-gateway)的步骤。把 [入口清单](../k8s/gateway.yaml) 与下表对照：

| 资源 | 在这个实验里的职责 |
| --- | --- |
| GatewayClass | 选择 Envoy Gateway 控制器 |
| Gateway | 声明 80 端口的 HTTP listener，只接收同命名空间的路由 |
| HTTPRoute | 精确匹配 `/api/shipping` 和 `/api/checkout`，分别引用两个 Service 的 80 端口 |
| EnvoyProxy | 将代理的 Service 配成 ClusterIP，便于在本地 kind 集群使用端口转发 |

```bash
kubectl apply -f k8s/gateway.yaml
kubectl wait --for=condition=Accepted gatewayclass/go-lab-envoy --timeout=120s
kubectl wait --for=condition=Programmed gateway/go-lab --timeout=120s
kubectl describe httproute go-lab
```

在 HTTPRoute 的状态中找 `Accepted=True` 和 `ResolvedRefs=True`。前者说明路由已附加到 Gateway，后者说明 Service 引用有效。

### 从 Windows 访问统一入口

在一个实验 Bash 窗口查找 Envoy 为这个 Gateway 生成的 Service，并启动端口转发。保持窗口开启，结束时按 Ctrl+C。

```bash
selector='gateway.envoyproxy.io/owning-gateway-name=go-lab,gateway.envoyproxy.io/owning-gateway-namespace=go-lab'
kubectl get deploy,svc -n envoy-gateway-system -l "$selector"
kubectl wait --for=condition=Available deployment -n envoy-gateway-system -l "$selector" --timeout=120s
proxy=$(kubectl get svc -n envoy-gateway-system -l "$selector" -o jsonpath='{.items[0].metadata.name}')
kubectl port-forward -n envoy-gateway-system "svc/$proxy" 8091:80
```

在 **PowerShell** 测试两条路由和未匹配路径。

```powershell
curl.exe --noproxy "*" -fsS "http://127.0.0.1:8091/api/shipping?kg=3"
curl.exe --noproxy "*" -fsS "http://127.0.0.1:8091/api/checkout?subtotal_cny=100&kg=3"
curl.exe --noproxy "*" -i "http://127.0.0.1:8091/api/unknown"
```

预期依次为报价 12 元、合计 112 元、HTTP 404。这里的 8091 是转发到代理的本机端口。基础教程的 8088 仍直接通往运费 Service。云环境中常由负载均衡器接入 Gateway。

### 模拟结算服务的下游配置错误

在另一个实验 Bash 窗口把结算服务的下游 DNS 名改成一个不存在的名称。暂时保持端口转发运行。

```bash
kubectl set env deployment/checkout SHIPPING_API_URL=http://shipping-missing.go-lab.svc.cluster.local/api/shipping
kubectl rollout status deployment/checkout --timeout=120s
curl -i 'http://127.0.0.1:8091/api/checkout?subtotal_cny=100&kg=3'
curl -fsS 'http://127.0.0.1:8091/api/shipping?kg=3'
kubectl get pods -l app=checkout
kubectl set env deployment/checkout SHIPPING_API_URL=http://shipping.go-lab.svc.cluster.local/api/shipping
kubectl rollout status deployment/checkout --timeout=120s
curl -fsS 'http://127.0.0.1:8091/api/checkout?subtotal_cny=100&kg=3'
```

预期错误配置时结算请求返回 502，运费请求仍成功。结算 Pod 的 `/healthz` 检查自己的 HTTP 进程，因此不会因下游故障反复重启。恢复配置后，结算请求应重新返回 112 元。

遇到 404 时先看 Host 和路径是否匹配。遇到 502 或 503 时，先看 HTTPRoute 状态和 Service 对应的 EndpointSlice，再沿调用方向查结算服务的下游地址。

```bash
kubectl describe gateway go-lab
kubectl describe httproute go-lab
kubectl get endpointslices -l kubernetes.io/service-name=checkout
kubectl get endpointslices -l kubernetes.io/service-name=shipping
kubectl set env deployment/checkout --list
```

完成练习后可以保留应用，也可以在结束端口转发后，用 `kubectl delete -f k8s/gateway.yaml` 和 `kubectl delete -f k8s/checkout.yaml` 清理这两章的对象。

### 安装或恢复 Envoy Gateway

在 **Bash** 的仓库根目录准备两个 Envoy 镜像。首次执行会拉取缺少的镜像。

```bash
bash scripts/load-lab-images.sh envoyproxy/gateway:v1.10.0 docker.io/envoyproxy/envoy:distroless-v1.40.2
```

如果 Bash 能访问 GitHub，可直接下载并校验固定版本的清单，再应用到集群。

```bash
(
  set -euo pipefail
  manifest=$(mktemp)
  trap 'rm -f -- "$manifest"' EXIT
  curl -fLsS https://github.com/envoyproxy/gateway/releases/download/v1.10.0/install.yaml -o "$manifest"
  printf '%s  %s\n' f87f4d59b983aa760da52a2a0002c612028106fe69beefdf3d30cfb03905944e "$manifest" | sha256sum -c -
  kubectl apply --server-side -f "$manifest"
)
kubectl rollout status -n envoy-gateway-system deployment/envoy-gateway --timeout=120s
```

如果只有 Windows 能访问 GitHub，也可以用 **PowerShell** 下载清单，再从 WSL 应用。

```powershell
$env:WSL_UTF8 = '1'
$manifest = Join-Path $env:TEMP 'envoy-gateway-v1.10.0-install.yaml'
curl.exe -fLsS https://github.com/envoyproxy/gateway/releases/download/v1.10.0/install.yaml -o $manifest
if ($LASTEXITCODE -ne 0 -or (Get-FileHash $manifest).Hash -ne 'f87f4d59b983aa760da52a2a0002c612028106fe69beefdf3d30cfb03905944e') {
    throw 'Envoy Gateway manifest download or checksum failed'
}
$linuxManifest = wsl -d K8s-Go-Lab --exec wslpath -a $manifest
wsl -d K8s-Go-Lab --exec kubectl apply --server-side -f $linuxManifest
wsl -d K8s-Go-Lab --exec kubectl rollout status -n envoy-gateway-system deployment/envoy-gateway --timeout=120s
```

恢复基础运费服务后，回到本页开头部署结算服务和 Gateway。

## 官方资料

- [Gateway API 的资源与职责](https://gateway-api.sigs.k8s.io/docs/concepts/api-overview/)
- [Envoy Gateway 版本兼容表](https://gateway.envoyproxy.io/news/releases/matrix/)
- [Envoy Gateway 官方 YAML 安装](https://gateway.envoyproxy.io/docs/install/install-yaml/)
- [EnvoyProxy 自定义配置](https://gateway.envoyproxy.io/docs/tasks/operations/customize-envoyproxy/)
