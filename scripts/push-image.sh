#!/usr/bin/env bash
set -euo pipefail
# Builds this checkout and pushes it to michaelcourcy/lvmo-csi only.
# Usage: push-image.sh TAG [PLATFORMS]   (default: linux/amd64,linux/arm64)
tag=${1:?usage: push-image.sh TAG [PLATFORMS]}
platforms=${2:-linux/amd64,linux/arm64}
[[ $tag =~ ^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$ ]] || { echo "invalid tag: $tag" >&2; exit 2; }
[[ $platforms =~ ^linux/[a-z0-9]+(,linux/[a-z0-9]+)*$ ]] || { echo "invalid platforms: $platforms" >&2; exit 2; }
cd "$(dirname "$0")/.."
docker buildx build --platform "$platforms" --build-arg VERSION="$tag" \
 -t "michaelcourcy/lvmo-csi:$tag" --push .
