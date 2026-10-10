#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
driver="$root/charts/lvmo-csi"
server="$root/charts/lvmo-csi-storage-server"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
absent() {
  if grep "$@"; then echo 'Unexpected rendered content'; exit 1; fi
}
api=(--api-versions snapshot.storage.k8s.io/v1/VolumeSnapshotClass)
helm lint "$driver" --set snapshotClass.enabled=false
helm lint "$server" --set source-storage-class=external
helm template test "$driver" "${api[@]}" >"$tmp/driver"
grep -q 'kind: VolumeSnapshotClass' "$tmp/driver"
grep -q 'name: "lvmo-snapshots"' "$tmp/driver"
grep -q 'k10.kasten.io/is-snapshot-class: "true"' "$tmp/driver"
absent -Eq 'kind: PersistentVolumeClaim|kind: StorageClass|name: test-storage' "$tmp/driver"
if helm template test "$driver" >"$tmp/missing" 2>&1; then echo 'Missing snapshot API accepted'; exit 1; fi
for text in 'Snapshot CRDs are missing' 'https://github.com/kubernetes-csi/external-snapshotter/tree/v8.5.0#usage' '--set snapshotClass.enabled=false'; do
  grep -Fq -- "$text" "$tmp/missing"
done
helm template test "$driver" --set snapshotClass.enabled=false >"$tmp/no-snapshots"
absent -q 'kind: VolumeSnapshotClass' "$tmp/no-snapshots"
helm template test "$driver" "${api[@]}" --set snapshotClass.name=custom-snapshots --set snapshotClass.deletionPolicy=Retain --set snapshotClass.kasten=false >"$tmp/custom"
grep -q 'name: "custom-snapshots"' "$tmp/custom"
grep -q 'deletionPolicy: Retain' "$tmp/custom"
absent -q 'k10.kasten.io/is-snapshot-class' "$tmp/custom"
for bad in snapshotClass.deletionPolicy=Keep snapshotClass.name= create-storage-server.enabled=true create-storage-server.enabled=false; do
  if helm template test "$driver" "${api[@]}" --set "$bad" >"$tmp/bad" 2>&1; then echo "Accepted invalid value: $bad"; exit 1; fi
done
args=(--set source-storage-class=external --namespace storage-test)
helm template test "$server" "${args[@]}" >"$tmp/server"
for text in 'name: test-iscsi' 'name: test-nfs' 'storage: 50Gi' 'helm.sh/resource-policy: keep' 'helm.sh/hook: pre-delete' 'type: Recreate' 'kind: Service' 'type: ClusterIP' 'hostNetwork: false' 'fieldPath: status.podIP' 'test-storage.storage-test.svc:50051' 'podAffinity:' 'port: 2049' 'port: 3260'; do
  grep -Fq "$text" "$tmp/server"
done
absent -Eq 'kind: VolumeSnapshotClass|kind: CSIDriver|kind: DaemonSet|name: lvmo-controller|helm.sh/hook: pre-upgrade|cpu:|memory:' "$tmp/server"
iscsi=$(awk 'BEGIN{RS="---\n"} /name: test-iscsi/' "$tmp/server")
nfs=$(awk 'BEGIN{RS="---\n"} /name: test-nfs/' "$tmp/server")
grep -q 'k10.kasten.io/sc-supports-block-mode-exports: "true"' <<<"$iscsi"
absent -q 'sc-supports-block-mode-exports' <<<"$nfs"
absent -Eq 'hostNetwork: true|NFS_CLIENTS|kubernetes.io/hostname:|server-address|node-name' "$tmp/server"
for bad in size=5GB source-storage-class= dest-storage-class-prefix=Bad_Prefix source-storage-class=test-nfs source-storage-class=test-iscsi "dest-storage-class-prefix=$(printf '%056d' 0)"; do
  if helm template test "$server" "${args[@]}" --set "$bad" >"$tmp/bad" 2>&1; then echo "Accepted invalid value: $bad"; exit 1; fi
done
absent -Eq 'volumeMode: Block|volumeDevices:|BLOCK_DEVICE|name: test-storage-state' "$tmp/server"
helm template test "$server" "${args[@]}" --set block-mode=true --set state-size=2Gi >"$tmp/block"
for text in 'volumeMode: Block' 'name: test-storage-state' 'storage: 2Gi' 'devicePath: /lvmo-dev/backing' 'name: BLOCK_DEVICE' 'name: block-device' 'lvmo.csi.io/block-mode: "true"'; do
  grep -Fq "$text" "$tmp/block"
done
absent -q 'BACKING_SIZE' "$tmp/block"
# Server and guard both keep API state on the Filesystem state PVC.
[[ $(grep -c 'claimName: test-storage-state' "$tmp/block") == 2 ]] || { echo 'State PVC not used by server and guard'; exit 1; }
[[ $(grep -c 'claimName: test-storage$' "$tmp/block") == 1 ]] || { echo 'Block PVC not used once'; exit 1; }
for bad in block-mode=yes state-size=1GB; do
  if helm template test "$server" "${args[@]}" --set-string "$bad" >"$tmp/bad" 2>&1; then echo "Accepted invalid value: $bad"; exit 1; fi
done
helm template test "$server" "${args[@]}" --set dest-storage-class-prefix=custom --set size=5Gi >"$tmp/override"
grep -q 'name: custom-iscsi' "$tmp/override"
grep -q 'storage: 5Gi' "$tmp/override"
for section in requests limits; do
  helm template test "$server" "${args[@]}" --set resources.$section.cpu=100m --set resources.$section.memory=256Mi >"$tmp/resources"
  grep -q 'cpu: 100m' "$tmp/resources"
  grep -q 'memory: 256Mi' "$tmp/resources"
  other=requests; [[ $section == requests ]] && other=limits
  # Inspect just the Deployment: PVC requests are unrelated to container resources.
  awk 'BEGIN{RS="---\n"} /kind: Deployment/' "$tmp/resources" >"$tmp/deployment"
  absent -q "$other:" "$tmp/deployment"
done
helm template test "$server" "${args[@]}" --set resources.requests.cpu=100m --set resources.requests.memory=256Mi --set resources.limits.cpu=2 --set resources.limits.memory=2Gi >"$tmp/resources"
for text in 'requests:' 'limits:' 'cpu: 100m' 'cpu: 2' 'memory: 256Mi' 'memory: 2Gi'; do grep -q "$text" "$tmp/resources"; done
for namespace in one two; do
  helm template test "$server" --set source-storage-class=external --set dest-storage-class-prefix="$namespace" --namespace "$namespace" >"$tmp/$namespace"
  grep -q "test-storage.$namespace.svc:50051" "$tmp/$namespace"
  awk '/name: VG/{getline; print}' "$tmp/$namespace" >"$tmp/$namespace-vg"
done
if cmp -s "$tmp/one-vg" "$tmp/two-vg"; then echo "Namespaces share a VG identity"; exit 1; fi
bash -n "$server/files/storage-server.sh"
bash -n "$server/files/exportfs"
bash -n "$root/scripts/quickstart-kind.sh"
echo 'Independent driver/server chart checks passed'
