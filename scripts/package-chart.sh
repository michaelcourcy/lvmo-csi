#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=${VERSION:-dev}
# Chart versions are SemVer; image tags retain the release's leading v.
chart_version=${version#v}
if [[ $version == dev ]]; then chart_version=0.0.0-dev; fi
helm package charts/lvmo-csi --destination "${1:-dist}" \
  --version "$chart_version" --app-version "$version"
