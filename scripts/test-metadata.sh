#!/usr/bin/env bash
set -euo pipefail
context=${KUBE_CONTEXT:-kind-lvmo-e2e}
ns=lvmo-cbt-test
mode=${TEST_SOURCE_MODE:-nfs}
sc=lvmo-nfs
volume_mode=Filesystem
access=ReadWriteMany
mount='volumeMounts: [{name: data, mountPath: /data}]'
writer_image=busybox:1.37
if [[ $mode != nfs ]]; then sc=lvmo-iscsi; access=ReadWriteOnce; fi
if [[ $mode == iscsi-block ]]; then
 volume_mode=Block
 mount='volumeDevices: [{name: data, devicePath: /dev/source}]'
 writer_image=${TEST_IMAGE:-michaelcourcy/lvmo-csi:dev}
fi
k() { kubectl --context "$context" "$@"; }
trap 'k delete namespace "$ns" --ignore-not-found --wait=true --timeout=120s; k delete clusterrolebinding lvmo-cbt-test --ignore-not-found; k delete clusterrole lvmo-cbt-test --ignore-not-found' EXIT
k create namespace "$ns"
k label namespace "$ns" pod-security.kubernetes.io/enforce=privileged
k -n "$ns" apply -f - <<EOF2
apiVersion: v1
kind: ServiceAccount
metadata: {name: backup}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: lvmo-cbt-test}
rules:
- apiGroups: [snapshot.storage.k8s.io]
  resources: [volumesnapshots, volumesnapshotcontents]
  verbs: [get, list]
- apiGroups: [cbt.storage.k8s.io]
  resources: [snapshotmetadataservices]
  verbs: [get, list]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: lvmo-cbt-test}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: lvmo-cbt-test}
subjects:
- {kind: ServiceAccount, name: backup, namespace: $ns}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: source}
spec:
  storageClassName: $sc
  volumeMode: $volume_mode
  accessModes: [$access]
  resources: {requests: {storage: 128Mi}}
---
apiVersion: v1
kind: Pod
metadata: {name: writer}
spec:
  containers:
  - name: writer
    image: $writer_image
    imagePullPolicy: IfNotPresent
    command: [sh, -c, 'sleep 3600']
    $mount
    securityContext: {privileged: true}
  volumes:
  - name: data
    persistentVolumeClaim: {claimName: source}
EOF2
if [[ ${OPENSHIFT:-false} == true ]]; then
 oc --context "$context" adm policy add-scc-to-user privileged -z default -n "$ns"
fi
k -n "$ns" wait --for=condition=Ready pod/writer --timeout=180s
for snap in a b; do
 if [[ $mode == iscsi-block ]]; then
  k -n "$ns" exec writer -- sh -c "printf snapshot-$snap | dd of=/dev/source bs=65536 seek=64 conv=sync,notrunc; sync"
  if [[ $snap == a ]]; then
   k -n "$ns" exec writer -- sh -c 'printf allocated | dd of=/dev/source bs=65536 seek=128 conv=sync,notrunc; sync'
  else
   k -n "$ns" exec writer -- blkdiscard --offset 8388608 --length 65536 /dev/source
  fi
 else
  k -n "$ns" exec writer -- sh -c "echo snapshot-$snap > /data/payload; sync"
 fi
 k -n "$ns" apply -f - <<EOF2
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata: {name: $snap}
spec:
  volumeSnapshotClassName: lvmo-snapshots
  source: {persistentVolumeClaimName: source}
EOF2
 k -n "$ns" wait --for=jsonpath='{.status.readyToUse}'=true volumesnapshot/"$snap" --timeout=180s
 content=$(k -n "$ns" get volumesnapshot "$snap" -o jsonpath='{.status.boundVolumeSnapshotContentName}')
 k annotate volumesnapshotcontent "$content" snapshot.storage.kubernetes.io/allow-volume-mode-change=true --overwrite
 if [[ $snap == a ]]; then base=$(k get volumesnapshotcontent "$content" -o jsonpath='{.status.snapshotHandle}'); fi
 k -n "$ns" apply -f - <<EOF2
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: clone-$snap}
spec:
  storageClassName: lvmo-iscsi
  volumeMode: Block
  accessModes: [ReadWriteOnce]
  resources: {requests: {storage: 128Mi}}
  dataSource: {apiGroup: snapshot.storage.k8s.io, kind: VolumeSnapshot, name: $snap}
EOF2
done
# Discover the service; no endpoint or CA is hard-coded in the client.
endpoint=$(k get snapshotmetadataservice lvmo.csi.io -o jsonpath='{.spec.address}')
work=$(mktemp -d)
k get snapshotmetadataservice lvmo.csi.io -o jsonpath='{.spec.caCert}' | base64 -d > "$work/ca.crt"
k -n "$ns" create configmap metadata-ca --from-file=ca.crt="$work/ca.crt"
rm -rf "$work"
k -n "$ns" apply -f - <<EOF2
apiVersion: v1
kind: Pod
metadata: {name: backup}
spec:
  serviceAccountName: backup
  containers:
  - name: backup
    image: ${TEST_IMAGE:-michaelcourcy/lvmo-csi:dev}
    imagePullPolicy: IfNotPresent
    command: [sh, -c, 'sleep 3600']
    securityContext: {runAsUser: 0}
    volumeDevices:
    - {name: a, devicePath: /dev/clone-a}
    - {name: b, devicePath: /dev/clone-b}
    volumeMounts:
    - {name: ca, mountPath: /tls}
    - {name: token, mountPath: /token}
    - {name: backup, mountPath: /backup}
  volumes:
  - name: a
    persistentVolumeClaim: {claimName: clone-a, readOnly: true}
  - name: b
    persistentVolumeClaim: {claimName: clone-b, readOnly: true}
  - name: ca
    configMap: {name: metadata-ca}
  - name: token
    projected:
      sources:
      - serviceAccountToken: {path: token, audience: lvmo.csi.io, expirationSeconds: 600}
  - name: backup
    emptyDir: {}
EOF2
if [[ ${OPENSHIFT:-false} == true ]]; then
 oc --context "$context" adm policy add-scc-to-user privileged -z backup -n "$ns"
fi
k -n "$ns" wait --for=condition=Ready pod/backup --timeout=180s
client=(/usr/local/bin/lvmo-metadata --endpoint="$endpoint" --namespace="$ns")
# Stop a stream early, then resume into the same partially reconstructed image.
checkpoint=$(k -n "$ns" exec backup -- "${client[@]}" --snapshot=a --device=/dev/clone-a --output=/backup/resume --max-batches=1 --verify=false)
offset=${checkpoint##*next_offset=}
k -n "$ns" exec backup -- "${client[@]}" --snapshot=a --device=/dev/clone-a --output=/backup/resume --offset="$offset"
k -n "$ns" exec backup -- "${client[@]}" --snapshot=a --device=/dev/clone-a
k -n "$ns" exec backup -- "${client[@]}" --snapshot=b --base="$base" --device=/dev/clone-b
# Continuation may omit already-applied ranges and must preserve the result.
k -n "$ns" exec backup -- "${client[@]}" --snapshot=b --base="$base" --device=/dev/clone-b --offset=65536
k -n "$ns" exec backup -- sh -c 'echo invalid > /backup/bad-token'
if k -n "$ns" exec backup -- "${client[@]}" --snapshot=a --device=/dev/clone-a --token-file=/backup/bad-token; then
 echo 'Unauthenticated metadata request unexpectedly succeeded' >&2; exit 1
fi
if k -n "$ns" exec backup -- "${client[@]}" --snapshot=does-not-exist --device=/dev/clone-a; then
 echo 'Invalid snapshot unexpectedly accepted' >&2; exit 1
fi
# Authenticated but unprivileged service account must also be rejected.
k -n "$ns" create serviceaccount denied
k -n "$ns" create token denied --audience=lvmo.csi.io --duration=10m | k -n "$ns" exec -i backup -- sh -c 'cat > /backup/denied-token'
if k -n "$ns" exec backup -- "${client[@]}" --snapshot=a --device=/dev/clone-a --token-file=/backup/denied-token; then
 echo 'Unauthorized metadata request unexpectedly succeeded' >&2; exit 1
fi
echo "Public KEP-3314 reconstruction, continuation, authentication and authorization passed: $mode"
