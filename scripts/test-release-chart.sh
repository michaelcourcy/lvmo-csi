#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
args=(--set create-storage-server.enabled=true --set create-storage-server.source-storage-class=external)
for version in v1.2.3 v1.2.3-rc.1 dev; do
  chart_version=${version#v}
  if [[ $version == dev ]]; then chart_version=0.0.0-dev; fi
  VERSION="$version" bash scripts/package-chart.sh "$tmp"
  chart="$tmp/lvmo-csi-$chart_version.tgz"
  helm show chart "$chart" >"$tmp/metadata"
  grep -Fxq "version: $chart_version" "$tmp/metadata"
  # Inspect the artifact, not the source chart: release metadata drives tags.
  helm template check "$chart" "${args[@]}" >"$tmp/defaults"
  [[ $(grep -Fc "image: \"michaelcourcy/lvmo-csi:$version\"" "$tmp/defaults") == 3 ]]
  [[ $(grep -Fc "image: \"michaelcourcy/lvmo-csi-storage-server:$version\"" "$tmp/defaults") == 3 ]]
  helm template check "$chart" "${args[@]}" \
    --set image.repository=example.test/driver --set image.tag=driver-test \
    --set create-storage-server.image.repository=example.test/server \
    --set create-storage-server.image.tag=server-test >"$tmp/overrides"
  [[ $(grep -Fc 'image: "example.test/driver:driver-test"' "$tmp/overrides") == 3 ]]
  [[ $(grep -Fc 'image: "example.test/server:server-test"' "$tmp/overrides") == 3 ]]
done
echo 'Release chart versions, default images and overrides passed'
