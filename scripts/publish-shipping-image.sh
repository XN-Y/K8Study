#!/usr/bin/env bash
set -euo pipefail

image=${1:-}
if [[ $# -ne 1 || ! $image =~ ^[a-z0-9][a-z0-9.-]*(:[0-9]+)?/[a-z0-9][a-z0-9._/-]*$ ]]; then
  printf 'Usage: bash scripts/publish-shipping-image.sh <registry/repository>\n' >&2
  exit 2
fi

cd "$(dirname "${BASH_SOURCE[0]}")/.."
commit=$(git rev-parse --verify HEAD)
if [[ -n $(git status --porcelain) ]]; then
  printf 'Commit source changes before publishing an image tagged with the Git SHA.\n' >&2
  exit 1
fi

metadata=$(mktemp)
trap 'rm -f -- "$metadata"' EXIT
docker buildx build --platform linux/amd64 --push \
  --build-arg "VERSION=$commit" \
  --label "org.opencontainers.image.revision=$commit" \
  --tag "$image:$commit" --metadata-file "$metadata" . >&2

digest=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["containerimage.digest"])' "$metadata")
if [[ ! $digest =~ ^sha256:[0-9a-f]{64}$ ]]; then
  printf 'Build did not return a valid image digest.\n' >&2
  exit 1
fi
printf '%s@%s\n' "$image" "$digest"
