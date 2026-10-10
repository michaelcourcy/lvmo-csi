#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
for version in v1.2.3 v1.2.3-rc.1 dev; do
  chart_version=${version#v}
  if [[ $version == dev ]]; then chart_version=0.0.0-dev; fi
  VERSION="$version" bash scripts/package-chart.sh "$tmp"
  for name in lvmo-csi lvmo-csi-storage-server; do
    chart="$tmp/$name-$chart_version.tgz"
    helm show chart "$chart" >"$tmp/metadata"
    grep -Fxq "name: $name" "$tmp/metadata"
    grep -Fxq "version: $chart_version" "$tmp/metadata"
    grep -Eq "^appVersion: [\"']?$version[\"']?$" "$tmp/metadata"
    if [[ $name == lvmo-csi ]]; then
      args=(--api-versions snapshot.storage.k8s.io/v1/VolumeSnapshotClass)
      count=3; override=driver
    else
      args=(--set source-storage-class=external)
      count=2; override=server
    fi
    helm template check "$chart" "${args[@]}" >"$tmp/defaults"
    [[ $(grep -Fc "image: \"michaelcourcy/$name:$version\"" "$tmp/defaults") == "$count" ]]
    helm template check "$chart" "${args[@]}" --set image.repository="example.test/$override" --set image.tag="$override-test" >"$tmp/overrides"
    [[ $(grep -Fc "image: \"example.test/$override:$override-test\"" "$tmp/overrides") == "$count" ]]
  done
done
helm repo index "$tmp" --url https://example.test/releases > /dev/null
for name in lvmo-csi lvmo-csi-storage-server; do
  for version in 1.2.3 1.2.3-rc.1 0.0.0-dev; do
    grep -Fq "$name-$version.tgz" "$tmp/index.yaml"
  done
done
echo 'Both release chart packages, versions, images and repository index passed'
