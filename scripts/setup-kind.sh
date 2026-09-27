#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
case $(uname -m) in aarch64) arch=arm64;; x86_64) arch=amd64;; esac
curl -fsSL "https://kind.sigs.k8s.io/dl/v0.31.0/kind-linux-$arch" -o /usr/local/bin/kind
curl -fsSL "https://dl.k8s.io/release/v1.35.0/bin/linux/$arch/kubectl" -o /usr/local/bin/kubectl
chmod +x /usr/local/bin/kind /usr/local/bin/kubectl
curl -fsSL "https://get.helm.sh/helm-v3.17.3-linux-$arch.tar.gz" | tar -xz -C /tmp
install "/tmp/linux-$arch/helm" /usr/local/bin/helm
curl -fsSL "https://dl.k8s.io/v1.35.0/kubernetes-test-linux-$arch.tar.gz" | tar -xz -C /tmp kubernetes/test/bin/e2e.test
install /tmp/kubernetes/test/bin/e2e.test /usr/local/bin/e2e.test
cat > /tmp/lvmo-kind.yaml <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraMounts:
  - hostPath: /proc
    containerPath: /run/lvmo-host-proc
    readOnly: true
YAML
kind create cluster --name lvmo-e2e --image kindest/node:v1.35.0 --config /tmp/lvmo-kind.yaml --wait 180s
# iSCSI operations use the outer VM's daemon; no nested iscsid is started.
cat > "$root/bin/Dockerfile" <<'DOCKER'
FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nfs-common open-iscsi util-linux e2fsprogs xfsprogs ca-certificates && rm -rf /var/lib/apt/lists/*
COPY lvmo-driver lvmo-metadata /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/lvmo-driver"]
DOCKER
if [[ -n ${RELEASE_VERSION:-} ]]; then
 docker pull "michaelcourcy/lvmo-csi:$RELEASE_VERSION"
 docker tag "michaelcourcy/lvmo-csi:$RELEASE_VERSION" michaelcourcy/lvmo-csi:dev
else
 docker build -t michaelcourcy/lvmo-csi:dev "$root/bin"
fi
kind load docker-image michaelcourcy/lvmo-csi:dev --name lvmo-e2e
API_ENDPOINT="${STORAGE_SERVER:-192.168.5.15}:50051" bash "$root/scripts/install-test-cluster.sh"
