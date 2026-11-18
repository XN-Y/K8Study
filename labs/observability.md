# 用指标和日志排查一次慢请求

[自动扩缩容实验](README.md#cpu-指标驱动自动扩缩容)里的 Metrics Server 可以看到容器 CPU 和内存。这里用 Prometheus 看 Go 应用如何响应请求，再拿日志定位一次运费服务超时。先按[双服务交付](two-services.md)部署结算服务。

## 读取 Go 应用的指标

结算服务 v2 已部署。它使用 Go 标准库在 `/metrics` 暴露 Prometheus 文本格式，并为每次结算请求输出 JSON 日志。打开 [metrics.go](../cmd/checkout/metrics.go) 和 [main.go](../cmd/checkout/main.go)，找出下面三组时间序列的写入位置。

| 指标 | 能回答的问题 |
| --- | --- |
| `checkout_http_requests_total` | 200、400、502 和 504 各返回了多少次？ |
| `checkout_http_request_duration_seconds` | 一次结算请求总共用了多久？ |
| `checkout_shipping_request_duration_seconds` | 调用运费依赖用了多久？ |

在一个实验 **Bash** 终端启动端口转发，并保持开启：

```bash
kubectl port-forward svc/checkout 8092:80
```

在另一个实验 Bash 终端生成一次正常请求和一次无效输入，再查看计数器：

```bash
checkout_url='http://127.0.0.1:8092/api/checkout?subtotal_cny=100&kg=3'
curl -fsS "$checkout_url"
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' 'http://127.0.0.1:8092/api/checkout?subtotal_cny=0&kg=3'
curl -fsS http://127.0.0.1:8092/metrics | grep '^checkout_http_requests_total'
kubectl logs deployment/checkout --tail=5
```

预期正常请求返回 112 元，无效输入返回 400。两种请求都会计入结算请求量，400 在校验输入时就结束，所以没有下游耗时样本。

**注意端口转发会绑定到选中的 Pod。** 后面的实验会更新 Deployment。若 8092 停止响应，在原窗口重新执行 `kubectl port-forward svc/checkout 8092:80`。

## 用 Prometheus 查询请求量、错误率和延迟

使用新集群时先准备 Prometheus v3.13.5 镜像，再应用[清单](../k8s/prometheus.yaml)。

```bash
bash scripts/load-lab-images.sh prom/prometheus:v3.13.5
kubectl apply -f k8s/prometheus.yaml
kubectl rollout status deployment/prometheus-lab --timeout=120s
```

在一个独立的 Bash 终端运行 `kubectl port-forward deployment/prometheus-lab 9090:9090`。用 Windows 浏览器打开 [Prometheus](http://127.0.0.1:9090/query)，然后逐条查询：

| 想看什么 | PromQL |
| --- | --- |
| 能否采集 | `up{job="checkout"}` |
| 最近一分钟每秒请求量 | `sum(rate(checkout_http_requests_total[1m]))` |
| 最近一分钟 5xx 占比 | `sum(rate(checkout_http_requests_total{code=~"5.."}[1m])) / sum(rate(checkout_http_requests_total[1m]))` |
| 结算总耗时 P95，单位秒 | `histogram_quantile(0.95, sum by (le) (rate(checkout_http_request_duration_seconds_bucket[1m])))` |
| 下游耗时 P95，单位秒 | `histogram_quantile(0.95, sum by (le) (rate(checkout_shipping_request_duration_seconds_bucket[1m])))` |

`up` 为 1 说明 Prometheus 成功抓到了指标。速率和 P95 至少要有两个采样点，并且窗口内有请求。P95 由直方图桶估算。这里每 5 秒抓单副本 checkout 的 Service，数据写入临时卷。多副本服务应发现并抓取每个实例，再按服务聚合。

## 制造一次下游超时

[慢响应清单](../k8s/slow-shipping.yaml) 会启动一个独立的模拟服务，收到连接后等待 5 秒才返回。让结算服务临时调用它。真实运费 Service 继续处理请求。

```bash
bash scripts/load-lab-images.sh busybox:1.37.0
kubectl apply -f k8s/slow-shipping.yaml
kubectl rollout status deployment/slow-shipping --timeout=90s
kubectl set env deployment/checkout SHIPPING_API_URL=http://slow-shipping.go-lab.svc.cluster.local/api/shipping
kubectl rollout status deployment/checkout --timeout=120s
```

重新启动 checkout 的端口转发，等约 10 秒让 Prometheus 采到新 Pod 的初始计数，再请求三次：

```bash
for i in {1..3}; do
  curl -sS -o /dev/null -w 'HTTP %{http_code}，耗时 %{time_total}s\n' "$checkout_url"
done
kubectl logs deployment/checkout --tail=5
```

预期每次在约 2 秒后返回 504。用 `checkout_http_requests_total{code="504"}` 查看超时计数，再对照日志中的 `duration_ms` 和 `shipping_ms`：两个值都接近 2000，说明这次时间主要花在等待下游。查询结算和下游的 P95，可以看到两个分布同步变慢。

练完恢复下游地址并清理模拟服务：

```bash
kubectl set env deployment/checkout SHIPPING_API_URL=http://shipping.go-lab.svc.cluster.local/api/shipping
kubectl rollout status deployment/checkout --timeout=120s
kubectl delete -f k8s/slow-shipping.yaml
```

重新建立 8092 转发后，请求应再次返回合计 112 元。Pod 替换会重置进程计数，Prometheus 中仍可查询之前的样本。需要结束实验时按 Ctrl+C 停止端口转发。

官方资料：[Prometheus 指标实践](https://prometheus.io/docs/practices/instrumentation/)、[直方图与分位数](https://prometheus.io/docs/practices/histograms/)。
