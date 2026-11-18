#!/usr/bin/env bash
set -euo pipefail

if [[ $# -eq 0 ]]; then
  printf 'Usage: bash scripts/load-lab-images.sh <image> [<image> ...]\n' >&2
  exit 2
fi

for image in "$@"; do
  if ! docker image inspect -- "$image" >/dev/null 2>&1; then
    docker pull -- "$image"
  fi
done
kind load docker-image --name go-lab -- "$@"
