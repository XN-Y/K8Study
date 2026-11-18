#!/usr/bin/env bash
set -euo pipefail

tag=${1:-}
if [[ $# -ne 1 || ! $tag =~ ^[a-z0-9][a-z0-9._-]{0,63}$ || $tag == latest ]]; then
  printf 'Usage: bash scripts/release-shipping.sh <unique-version-tag>\n' >&2
  exit 2
fi

cd "$(dirname "${BASH_SOURCE[0]}")/.."
image="go-shipping:$tag"
kubectl_lab=(kubectl --context=kind-go-lab --namespace=go-lab)

lock_dir="${XDG_CACHE_HOME:-$HOME/.cache}/k8s-go-lab"
mkdir -p "$lock_dir"
exec 9>"$lock_dir/shipping-release.lock"
if ! flock -n 9; then
  printf 'Another shipping release is running.\n' >&2
  exit 1
fi

current_image=$("${kubectl_lab[@]}" get deployment/shipping -o jsonpath='{.spec.template.spec.containers[?(@.name=="api")].image}')
if [[ $current_image == "$image" ]] || docker image inspect "$image" >/dev/null 2>&1; then
  printf 'Image tag already in use: %s\n' "$image" >&2
  exit 1
fi

printf 'Testing Go packages\n'
docker run --rm --mount "type=bind,source=$PWD,target=/src,readonly" \
  --workdir /src golang:1.26.10-alpine go test ./...

printf 'Building and importing %s\n' "$image"
docker build --pull=false --build-arg "VERSION=$tag" -t "$image" .
kind load docker-image "$image" --name go-lab

printf 'Updating shipping from %s to %s\n' "$current_image" "$image"
"${kubectl_lab[@]}" set image deployment/shipping "api=$image"
"${kubectl_lab[@]}" rollout status deployment/shipping --timeout=120s
