#!/usr/bin/env bash
set -euo pipefail
# Run on a dedicated Linux host with rootful Docker and host prerequisites.
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
[[ $(uname -s) == Linux ]] || { echo 'Run this inside the Linux test VM'; exit 1; }
for tool in docker kind kubectl helm; do command -v "$tool" >/dev/null; done
if kind get clusters | grep -qx lvmo-pod; then echo 'Cluster lvmo-pod already exists; refusing to overwrite'; exit 1; fi
mkdir -p .test/kind
for module in iscsi_tcp loop dm_thin_pool nfsd iscsi_target_mod; do sudo modprobe "$module"; done
sudo systemctl start iscsid
sudo mkdir -p /var/lib/lvmo-kind-source
docker build -f examples/kind/Dockerfile -t lvmo-kind-node:v1.35.0 .
kind create cluster --name lvmo-pod --image lvmo-kind-node:v1.35.0 --config examples/kind/cluster.yaml --wait 180s
docker build -t lvmo-local:driver .
docker build -f Dockerfile.storage-server -t lvmo-local:server .
kind load docker-image --name lvmo-pod lvmo-local:driver lvmo-local:server
context=kind-lvmo-pod
for crd in volumesnapshotclasses volumesnapshotcontents volumesnapshots; do
  kubectl --context "$context" apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v8.5.0/client/config/crd/snapshot.storage.k8s.io_${crd}.yaml"
done
for manifest in rbac-snapshot-controller.yaml setup-snapshot-controller.yaml; do
  kubectl --context "$context" apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v8.5.0/deploy/kubernetes/snapshot-controller/${manifest}"
done
cat >.test/kind/values.yaml <<VALUES
image:
  repository: lvmo-local
  tag: driver
iscsiHostProc: /run/lvmo-host-proc
create-storage-server:
  enabled: true
  source-storage-class: standard
  size: 5Gi
  dest-storage-class-prefix: lvmo-test-sc
  image:
    repository: lvmo-local
    tag: server
VALUES
helm upgrade --install lvmo charts/lvmo-csi --kube-context "$context" -n lvmo-system --create-namespace -f .test/kind/values.yaml --wait --timeout 5m
# Nested Kind uses the Linux host's iscsid. Give that namespace a route to
# this single Service through the Kind node, where kube-proxy performs DNAT.
service_ip=$(kubectl --context "$context" -n lvmo-system get service lvmo-storage -o jsonpath='{.spec.clusterIP}')
kind_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' lvmo-pod-control-plane)
sudo ip route add "$service_ip/32" via "$kind_ip"
kubectl --context "$context" get pods -n lvmo-system
kubectl --context "$context" get storageclass lvmo-test-sc-iscsi lvmo-test-sc-nfs
