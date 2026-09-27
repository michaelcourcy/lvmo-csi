#!/usr/bin/env bash
set -euo pipefail
# Local Linux acceptance: independent API processes, state directories and VGs.
# API B shares the test VM's network but is selected only by its SC endpoint.
root=$(cd "$(dirname "$0")/.." && pwd)
context=${KUBE_CONTEXT:-kind-lvmo-e2e}
server=${STORAGE_SERVER:-192.168.5.15}
api_a="$server:50051"
api_b="$server:50052"
name="routing-$(date +%s)"
state=/var/lib/lvmo-route
file=/var/lib/lvmo-route.img
loop=''
fixtures=false
k() { kubectl --context "$context" "$@"; }
controller() { k -n lvmo-system get pods -l app=lvmo-controller -o jsonpath='{.items[0].metadata.name}'; }
phase() {
 local pod
 pod=$(controller)
 k -n lvmo-system cp "$root/bin/integration.test" "$pod:/tmp/routing.test" -c driver
 k -n lvmo-system exec "$pod" -c driver -- env CSI_ENDPOINT=unix:///csi/csi.sock ROUTING_PHASE="$1" ROUTING_API_A="$api_a" ROUTING_API_B="$api_b" ROUTING_NAME="$name" /tmp/routing.test -test.run='^TestClusterBackendRouting$' -test.v
}
cleanup() {
 status=$?
 trap - EXIT
 if [[ $fixtures == true ]]; then phase cleanup || true; fi
 k delete storageclass lvmo-routing-nfs lvmo-routing-iscsi --ignore-not-found || true
 systemctl stop lvmo-route-api || true
 # On a failed test, leave uncertain storage for the enclosing disposable-VM
 # teardown instead of forcibly removing potentially mounted filesystems.
 if [[ $status == 0 ]]; then
  vgremove --yes lvmo-route
  pvremove --yes "$loop"
  losetup -d "$loop"
  rm -rf "$state" "$file"
 fi
 exit "$status"
}
[[ ! -e $state && ! -e $file ]] || { echo 'Routing test storage already exists' >&2; exit 1; }
trap cleanup EXIT
truncate -s 2G "$file"
loop=$(losetup --find --show "$file")
pvcreate --yes "$loop"
vgcreate lvmo-route "$loop"
lvcreate --yes --type thin-pool -L 1536M --poolmetadatasize 64M -n lvmo-pool lvmo-route
systemd-run --unit=lvmo-route-api --collect /usr/local/bin/lvmo-csi -P 50052 --root="$state" --server="$server" lvmo-route
for attempt in $(seq 1 60); do
 if (echo >/dev/tcp/127.0.0.1/50052) 2>/dev/null; then break; fi
 sleep 1
done
k apply -f - <<YAML
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata: {name: lvmo-routing-nfs}
provisioner: lvmo.csi.io
parameters: {endpoint: "$api_b", protocol: nfs, vg: lvmo-route}
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata: {name: lvmo-routing-iscsi}
provisioner: lvmo.csi.io
parameters: {endpoint: "$api_b", protocol: iscsi, vg: lvmo-route}
YAML
# The deployed driver must have no fallback address masking a missing SC route.
args=$(k -n lvmo-system get deployment lvmo-controller -o jsonpath='{.spec.template.spec.containers[?(@.name=="driver")].args}')
[[ $args == *'"--api-endpoint="'* ]] || { echo 'Unexpected default backend in cluster driver' >&2; exit 1; }
fixtures=true
phase create
k -n lvmo-system rollout restart deployment/lvmo-controller daemonset/lvmo-node
k -n lvmo-system rollout status deployment/lvmo-controller --timeout=180s
k -n lvmo-system rollout status daemonset/lvmo-node --timeout=180s
phase check
phase cleanup
fixtures=false
before=$(jq .next_snapshot_order /var/lib/lvmo/state.json)
TEST_NFS_CLASS=lvmo-routing-nfs TEST_ISCSI_CLASS=lvmo-routing-iscsi TEST_SOURCE_MODE=nfs bash "$root/scripts/test-metadata.sh"
after=$(jq .next_snapshot_order /var/lib/lvmo/state.json)
[[ $before == "$after" ]] || { echo 'Second-backend metadata workflow touched primary snapshots' >&2; exit 1; }
for attempt in $(seq 1 60); do
 if jq -e '(.volumes|length)==0 and (.snapshots|length)==0 and (.owners|length)==0' "$state/state.json" >/dev/null; then break; fi
 sleep 2
done
jq -e '(.volumes|length)==0 and (.snapshots|length)==0 and (.owners|length)==0' "$state/state.json" >/dev/null
[[ -z $(lvs --noheadings -o lv_name --select lv_tags=lvmo lvmo-route | tr -d '[:space:]') ]]
echo 'Per-StorageClass routing, cold discovery, second-backend CBT, and cleanup passed.'
