#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=${VERSION:-dev}
mkdir -p dist
for arch in amd64 arm64; do
 for cmd in lvmo-csi lvmo-driver lvmo-metadata; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "dist/$cmd-linux-$arch" "./cmd/$cmd"
 done
done
(cd dist; shasum -a 256 lvmo-*-linux-* > SHA256SUMS)
VERSION="$version" bash scripts/package-chart.sh dist
