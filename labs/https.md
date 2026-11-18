# 给 Gateway 加上 HTTPS

用[双服务交付](two-services.md)中的 Envoy Gateway 控制器，为结算接口增加实验域名 `checkout.k8s.test`。客户端和 Envoy 之间通过 HTTPS 通信，Envoy 完成 TLS 握手后根据域名和路径把请求转发到 checkout Service 的 HTTP 端口。

## 准备证书和 TLS Secret

这个实验用一张有效期 30 天的自签名证书。证书的 SAN 写明域名，私钥放在 Ubuntu 的用户目录，供 TLS Secret 使用。集群里已有有效证书时，可以直接[启动转发](#应用-https-入口并启动转发)。在实验 **Bash** 运行下面的命令，可以签发证书并更新 Secret。

```bash
cert_dir="$HOME/.local/share/k8s-go-lab/tls"
(
  umask 077
  mkdir -p "$cert_dir"
  openssl req -x509 -newkey rsa:2048 -noenc -sha256 -days 30 \
    -subj '/CN=checkout.k8s.test' -addext 'subjectAltName=DNS:checkout.k8s.test' \
    -addext 'extendedKeyUsage=serverAuth' -addext 'basicConstraints=critical,CA:FALSE' \
    -keyout "$cert_dir/tls.key" -out "$cert_dir/tls.crt"
)
openssl x509 -in "$cert_dir/tls.crt" -noout -dates -ext subjectAltName
kubectl -n go-lab create secret tls checkout-lab-tls \
  --cert="$cert_dir/tls.crt" --key="$cert_dir/tls.key" --dry-run=client -o yaml \
  | kubectl -n go-lab apply --server-side -f -
```

这条管道把证书和私钥直接送给 Kubernetes。用 `kubectl -n go-lab get secret checkout-lab-tls` 可以检查类型是 `kubernetes.io/tls`。

## 应用 HTTPS 入口并启动转发

从项目根目录应用 [HTTPS 清单](../k8s/gateway-https.yaml)。Gateway 在 443 监听 `checkout.k8s.test` 并引用刚才的 Secret，HTTPRoute 通过 `sectionName: https` 绑定该 listener。

```bash
kubectl apply -f k8s/gateway-https.yaml
kubectl -n go-lab wait --for=condition=Programmed gateway/go-lab-https --timeout=120s
kubectl -n go-lab describe httproute checkout-https

selector='gateway.envoyproxy.io/owning-gateway-name=go-lab-https,gateway.envoyproxy.io/owning-gateway-namespace=go-lab'
kubectl wait --for=condition=Available deployment -n envoy-gateway-system -l "$selector" --timeout=120s
proxy=$(kubectl get svc -n envoy-gateway-system -l "$selector" -o jsonpath='{.items[0].metadata.name}')
kubectl port-forward -n envoy-gateway-system "svc/$proxy" 8443:443
```

保持这个终端开启。HTTPRoute 的状态应包含 `Accepted=True` 和 `ResolvedRefs=True`。

## 从 Windows 验证

在 **PowerShell** 复制公开证书并调用结算接口。每次轮换证书后重新执行这段。

```powershell
$env:WSL_UTF8 = '1'
$cert = Join-Path $env:TEMP 'k8s-go-lab-checkout.crt'
wsl -d K8s-Go-Lab --exec cat /home/lab/.local/share/k8s-go-lab/tls/tls.crt |
    Set-Content -LiteralPath $cert -Encoding ascii
curl.exe --noproxy "*" --cacert $cert --resolve "checkout.k8s.test:8443:127.0.0.1" `
    -fsS "https://checkout.k8s.test:8443/api/checkout?subtotal_cny=100&kg=3"
```

预期返回 `total_cny: 112`。`--resolve` 将这次请求发到本机，同时保留 URL 中的域名供 TLS 和 HTTP 路由匹配。`--cacert` 只为这次调用提供信任材料。

去掉 `--cacert $cert`，观察自签名证书的信任错误。加回证书参数，把路径换成 `/api/unknown`，用 `-i` 查看 HTTP 404。也可以保留原 URL 并加上 `-H "Host: other.k8s.test"`，让 TLS 握手使用正确域名，同时验证 HTTP 主机名不匹配时的 404。

结束转发时按 Ctrl+C。证书过期后回到本页开头轮换 Secret，并重新复制公钥证书。重建集群时先按[双服务交付](two-services.md)恢复控制器和原有入口，再部署 HTTPS。

生产环境通常用公开或组织认可的 CA 为真实域名签发证书，并配置续期。这里的 `kubectl port-forward` 提供本机入口，生产入口通常经由负载均衡器接入 Gateway。参见 [Gateway API TLS 配置](https://gateway-api.sigs.k8s.io/guides/user-guides/tls/)和 [Kubernetes TLS Secret 命令](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_create/kubectl_create_secret_tls/)。
