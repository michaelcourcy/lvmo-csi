#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=${VERSION:-dev}
# Chart versions are SemVer; image tags retain the release's leading v.
chart_version=${version#v}
if [[ $version == dev ]]; then chart_version=0.0.0-dev; fi
for chart in lvmo-csi lvmo-csi-storage-server; do
  helm package "charts/$chart" --destination "${1:-dist}" \
    --version "$chart_version" --app-version "$version"
done
