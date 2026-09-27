#!/usr/bin/env bash
set -euo pipefail
# Run as root inside the dedicated Lima VM.
root=$(cd "$(dirname "$0")/.." && pwd)
context=${KUBE_CONTEXT:-kind-lvmo-e2e}
kubectl --context "$context" create namespace lvmo-system --dry-run=client -o yaml | kubectl --context "$context" apply -f -
kubectl --context "$context" label namespace lvmo-system pod-security.kubernetes.io/enforce=privileged --overwrite
for crd in volumesnapshotclasses volumesnapshotcontents volumesnapshots; do
 kubectl --context "$context" apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v8.5.0/client/config/crd/snapshot.storage.k8s.io_${crd}.yaml"
done
for manifest in rbac-snapshot-controller.yaml setup-snapshot-controller.yaml; do
 kubectl --context "$context" apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v8.5.0/deploy/kubernetes/snapshot-controller/${manifest}"
done
helm upgrade --install lvmo "$root/charts/lvmo-csi" --kube-context "$context" -n lvmo-system --set apiEndpoint="${API_ENDPOINT:-192.168.5.15:50051}" --set iscsiHostProc=/run/lvmo-host-proc --wait --timeout 5m
kubectl --context "$context" apply -f "$root/tests/storageclasses.yaml"
