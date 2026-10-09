#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
chart="$root/charts/lvmo-csi"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
helm lint "$chart"
helm template test "$chart" >"$tmp/off"
if grep -q 'name: test-storage' "$tmp/off"; then echo 'disabled chart creates server resources'; exit 1; fi
args=(--set create-storage-server.enabled=true --set create-storage-server.source-storage-class=external --set create-storage-server.image.repository=test)
helm template test "$chart" "${args[@]}" --namespace storage-test --show-only templates/storage-server.yaml >"$tmp/on"
for text in 'name: lvmo-test-sc-iscsi' 'name: lvmo-test-sc-nfs' 'helm.sh/resource-policy: keep' 'helm.sh/hook: pre-delete' 'helm.sh/hook: pre-upgrade' 'type: Recreate' 'kind: Service' 'type: ClusterIP' 'hostNetwork: false' 'fieldPath: status.podIP' 'test-storage.storage-test.svc:50051' 'podAffinity:' 'port: 2049' 'port: 3260'; do
  grep -q "$text" "$tmp/on"
done
if grep -Eq 'hostNetwork: true|NFS_CLIENTS|kubernetes.io/hostname:|server-address|node-name' "$tmp/on"; then
  echo 'server still requires host networking or explicit placement'; exit 1
fi
for bad in create-storage-server.size=5GB create-storage-server.source-storage-class= create-storage-server.dest-storage-class-prefix=Bad_Prefix create-storage-server.source-storage-class=lvmo-test-sc-nfs; do
  if helm template test "$chart" "${args[@]}" --set "$bad" >"$tmp/bad" 2>&1; then echo "Accepted invalid value: $bad"; exit 1; fi
done
bash -n "$chart/files/storage-server.sh"
bash -n "$chart/files/exportfs"
bash -n "$root/scripts/quickstart-kind.sh"
echo 'Pod storage chart checks passed'
