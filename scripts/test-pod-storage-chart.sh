#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
chart="$root/charts/lvmo-csi"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
helm lint "$chart"
helm template test "$chart" >"$tmp/off"
if grep -q 'name: test-storage' "$tmp/off"; then echo 'disabled chart creates server resources'; exit 1; fi
args=(--set create-storage-server.enabled=true --set create-storage-server.source-storage-class=external --set create-storage-server.node-name=worker --set create-storage-server.server-address=192.0.2.1 --set create-storage-server.image.repository=test --set create-storage-server.nfs-clients=192.0.2.2)
helm template test "$chart" "${args[@]}" >"$tmp/on"
for text in 'name: lvmo-test-sc-iscsi' 'name: lvmo-test-sc-nfs' 'helm.sh/resource-policy: keep' 'helm.sh/hook: pre-delete' 'helm.sh/hook: pre-upgrade' 'type: Recreate'; do
  grep -q "$text" "$tmp/on"
done
for bad in create-storage-server.size=5GB create-storage-server.source-storage-class= create-storage-server.dest-storage-class-prefix=Bad_Prefix create-storage-server.source-storage-class=lvmo-test-sc-nfs; do
  if helm template test "$chart" "${args[@]}" --set "$bad" >"$tmp/bad" 2>&1; then echo "Accepted invalid value: $bad"; exit 1; fi
done
bash -n "$chart/files/storage-server.sh"
bash -n "$chart/files/exportfs"
bash -n "$root/scripts/quickstart-kind.sh"
echo 'Pod storage chart checks passed'
