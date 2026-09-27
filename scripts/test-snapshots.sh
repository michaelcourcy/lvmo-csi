#!/usr/bin/env bash
set -euo pipefail
# Runs the manifests from the pinned upstream suite, with readiness and data assertions.
context=${KUBE_CONTEXT:-kind-lvmo-e2e}
sc=${STORAGE_CLASS:-lvmo-nfs}
revision=4556fe4061f6711653160aad910e990c8a0b5188
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fsSL "https://github.com/michaelcourcy/test-csi-snapshot/archive/${revision}.tar.gz" | tar -xz -C "$work" --strip-components=1
export NAMESPACE=lvmo-snapshot-test ANOTHER_NAMESPACE=lvmo-snapshot-restore STORAGE_CLASS="$sc" VOLUME_SNAPSHOT_CLASS=lvmo-snapshots CSI_DRIVER=lvmo.csi.io
k() { kubectl --context "$context" "$@"; }
cleanup() {
 k delete namespace "$ANOTHER_NAMESPACE" "$NAMESPACE" --ignore-not-found --wait=true --timeout=120s
 k delete volumesnapshotcontent restored-snapcontent --ignore-not-found
 rm -rf "$work"
}
trap cleanup EXIT
k create namespace "$NAMESPACE"
k create namespace "$ANOTHER_NAMESPACE"
if [[ ${OPENSHIFT:-false} == true ]]; then
 oc --context "$context" adm policy add-scc-to-user nonroot-v2 -z default -n "$NAMESPACE"
 oc --context "$context" adm policy add-scc-to-user nonroot-v2 -z default -n "$ANOTHER_NAMESPACE"
fi
for file in "$work"/01_restore-in-same-namespace/*.yaml; do
 envsubst < "$file" | k apply -f -
 case "$file" in
 *02_*)
  k -n "$NAMESPACE" wait --for=condition=Ready pod/pod-on-original-pvc --timeout=180s
  # Flush the initiator's filesystem before the storage-server snapshot.
  k -n "$NAMESPACE" exec pod-on-original-pvc -- sh -ec 'for i in 1 2 3 4 5 6 7 8 9 10; do test -f /data/test-file && break; sleep 1; done; test "$(cat /data/test-file)" = "test data"; sync'
  ;;
 *03_*) k -n "$NAMESPACE" wait --for=jsonpath='{.status.readyToUse}'=true volumesnapshot/snap-original-pvc --timeout=180s ;;
 esac
done
k -n "$NAMESPACE" wait --for=condition=Ready pod/pod-on-pvc-clone-from-snap-original-pvc --timeout=180s
[[ $(k -n "$NAMESPACE" exec pod-on-pvc-clone-from-snap-original-pvc -- cat /data/test-file) == 'test data' ]]
content=$(k -n "$NAMESPACE" get volumesnapshot snap-original-pvc -o jsonpath='{.status.boundVolumeSnapshotContentName}')
export SNAP_HANDLE
SNAP_HANDLE=$(k get volumesnapshotcontent "$content" -o jsonpath='{.status.snapshotHandle}')
for file in "$work"/02_restore-in-another-namespace/*.yaml; do
 envsubst < "$file" | k apply -f -
done
# The imported reference does not own the underlying snapshot's lifetime.
k patch volumesnapshotcontent restored-snapcontent --type=merge -p '{"spec":{"deletionPolicy":"Retain"}}'
k -n "$ANOTHER_NAMESPACE" wait --for=condition=Ready pod/pod-on-pvc-clone-from-restored-snap --timeout=180s
[[ $(k -n "$ANOTHER_NAMESPACE" exec pod-on-pvc-clone-from-restored-snap -- cat /data/test-file) == 'test data' ]]
echo "Upstream snapshot suite passed for $sc (same namespace and cross-namespace)."
