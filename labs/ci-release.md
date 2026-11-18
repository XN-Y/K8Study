# GitHub Actions 和镜像仓库实战

[本地一键发布](../README.md#一键发布与-ci)从本机 Docker 直接向 kind 导入镜像。这一节把同一个 Go 运费服务发布到镜像仓库，再让 Kubernetes 按镜像摘要拉取。

```text
Git 提交 → Go 测试与静态检查 → Docker 构建 → 推送带提交号的镜像
  → 记录镜像摘要 → Kubernetes 按摘要拉取 → 验证接口与回滚
```

## GitHub Actions 工作流

打开 [shipping-ci.yml](../.github/workflows/shipping-ci.yml)，找到 `verify` 和 `publish` 两个任务。

| 触发方式 | 运行内容 |
| --- | --- |
| 拉取请求 | 测试并检查 Go 代码，构建两个服务的镜像，在新 kind 集群验收运费入口和结算调用 |
| 推送到 `main` | 验证通过后，将运费镜像推送到 GHCR |
| 手动运行 | 执行验证，选中 `main` 时还会发布 |

发布任务使用 GitHub 提供的 `GITHUB_TOKEN` 和 `packages: write` 权限。[发布脚本](../scripts/publish-shipping-image.sh) 读取当前提交号，拒绝未提交的工作区，把 SHA 写入镜像标签、OCI revision 标签和运费接口的 `version`。任务会从 GHCR 按摘要拉取镜像，运行报表命令并核对版本，再将验证过的 `ghcr.io/xn-y/k8study-shipping@sha256:…` 写到运行摘要。

在 [K8Study 的 Actions](https://github.com/XN-Y/K8Study/actions/workflows/shipping-ci.yml) 查看运行结果。推送到 `main` 会触发流水线，也可以在该页面手动运行。镜像仓库名称由当前仓库名生成，fork 后会发布到 fork 的命名空间。

## 本机演练推送和拉取

以下步骤使用独立的 kind 集群 `go-lab-ci`，用单独的 kubeconfig 指向新集群。启动仓库容器时，Docker 会拉取缺少的 `registry:3.1.2` 镜像。

镜像标签来自真实 Git 提交。在实验 **Bash** 进入仓库根目录，确认 `git status --short` 没有输出。如果修改了源码，先把改动提交到你的分支，再进行本地演练。

```bash
ci_dir=$(mktemp -d)
ci=(kubectl --kubeconfig "$ci_dir/kubeconfig")
kind create cluster --name go-lab-ci --kubeconfig "$ci_dir/kubeconfig" \
  --config k8s/kind-registry.yaml --image kindest/node:v1.36.4 --wait 120s
docker run -d --rm --name go-lab-ci-registry --network kind \
  -p 127.0.0.1:5001:5000 registry:3.1.2

registry_dir=/etc/containerd/certs.d/localhost:5001
docker exec go-lab-ci-control-plane mkdir -p "$registry_dir"
printf '[host."http://go-lab-ci-registry:5000"]\n  capabilities = ["pull", "resolve"]\n' \
  | docker exec -i go-lab-ci-control-plane cp /dev/stdin "$registry_dir/hosts.toml"
curl -fsS --retry 8 --retry-connrefused http://127.0.0.1:5001/v2/
```

从 WSL 看，仓库位于 `localhost:5001`。节点是另一个 Docker 容器，containerd 的 `hosts.toml` 把同一个镜像地址映射到 kind 网络里的仓库容器。

```bash
commit=$(git rev-parse HEAD)
image=$(bash scripts/publish-shipping-image.sh localhost:5001/go-shipping)
printf '%s\n' "$image"
"${ci[@]}" create deployment shipping-ci --image="$image"
"${ci[@]}" rollout status deployment/shipping-ci --timeout=120s
pod=$("${ci[@]}" get pod -l app=shipping-ci -o jsonpath='{.items[0].metadata.name}')
"${ci[@]}" get pod "$pod" -o jsonpath='{.spec.containers[0].image}{"\n"}'
"${ci[@]}" get --raw "/api/v1/namespaces/default/pods/$pod:8080/proxy/api/shipping?kg=3"
printf 'Expected version: %s\n' "$commit"
```

验收时，Pod 的镜像引用应包含 `@sha256:`，HTTP 接口应返回 12 元和完整的 Git 提交号。想练习回滚，可以修改业务代码并提交第二次，重新运行发布脚本，然后执行：

```bash
next_image=$(bash scripts/publish-shipping-image.sh localhost:5001/go-shipping)
"${ci[@]}" set image deployment/shipping-ci go-shipping="$next_image"
"${ci[@]}" rollout status deployment/shipping-ci --timeout=120s
"${ci[@]}" rollout undo deployment/shipping-ci
"${ci[@]}" rollout status deployment/shipping-ci --timeout=120s
"${ci[@]}" get deployment shipping-ci -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
```

回滚后应看到第一份镜像摘要。练完清理本节的临时集群、仓库及 kubeconfig：

```bash
kind delete cluster --name go-lab-ci --kubeconfig "$ci_dir/kubeconfig"
docker stop go-lab-ci-registry
rm -f "$ci_dir/kubeconfig"
rmdir "$ci_dir"
```

## 用 Actions 产出的摘要部署

GitHub 上的发布成功后，把运行摘要中的 `Deployment image` 写进目标环境的 Deployment。要在当前实验集群体验发布，先确认节点可以访问 GHCR，然后在 **Bash** 以实际摘要替换下面的占位内容。

```bash
image='ghcr.io/xn-y/k8study-shipping@sha256:<实际摘要>'
kubectl --context kind-go-lab -n go-lab set image deployment/shipping "api=$image"
kubectl --context kind-go-lab -n go-lab rollout status deployment/shipping --timeout=120s
curl -fsS 'http://127.0.0.1:8088/api/shipping?kg=3'
```

私有 GHCR 包需要给 Pod 配置能读取包的 `imagePullSecrets`。Actions 的发布身份和 Kubernetes 节点的拉取身份各自配置。若 rollout 失败，先用 `kubectl describe pod` 看是拉取权限、网络还是探针问题，再按[发布与回滚](../README.md#发布-v2-并回滚)里的步骤恢复。

团队交付时，将摘要更新到版本库中的环境清单，发布后保留提交号、镜像摘要和健康检查结果。这样能从运行中的 Pod 追溯到构建，也能指定上一份摘要恢复部署。

官方资料：[GitHub 发布 Docker 镜像](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)、[Docker Buildx 元数据](https://docs.docker.com/reference/cli/docker/buildx/build/#metadata-file)、[kind 本地镜像仓库](https://kind.sigs.k8s.io/docs/user/local-registry/)。
