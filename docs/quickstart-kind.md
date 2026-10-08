# Quickstart: lvmo entirely inside Kind

Validated on 8 October 2026 in an Ubuntu 24.04 ARM64 Lima VM. This guide runs
a single test storage-server Pod backed
by Kind's local-path provisioner, then exposes NFS and iSCSI StorageClasses.
It is for a dedicated Linux host or VM, not production or failure testing.
Kind nodes share a kernel; a custom node image cannot supply missing kernel
features. Docker Desktop alone is not the tested platform.

## Linux prerequisites

Use Ubuntu 24.04 with rootful Docker, at least 4 CPUs, 6GiB RAM and 40GiB disk.
Clone this repository and run commands from its root.
Install the pinned CLI versions below. The backing PVC uses Kind’s existing
local-path class, independently of lvmo.

```sh
case $(uname -m) in aarch64) arch=arm64;; x86_64) arch=amd64;; esac
curl -fsSL "https://kind.sigs.k8s.io/dl/v0.31.0/kind-linux-$arch" -o /tmp/kind
curl -fsSL "https://dl.k8s.io/release/v1.35.0/bin/linux/$arch/kubectl" -o /tmp/kubectl
sudo install -m 0755 /tmp/kind /tmp/kubectl /usr/local/bin/
curl -fsSL "https://get.helm.sh/helm-v3.18.6-linux-$arch.tar.gz" | tar -xz -C /tmp
sudo install "/tmp/linux-$arch/helm" /usr/local/bin/helm
```

```sh
sudo apt-get update
sudo apt-get install -y open-iscsi nfs-common "linux-modules-extra-$(uname -r)"
for module in iscsi_tcp loop dm_thin_pool nfsd iscsi_target_mod; do
  sudo modprobe "$module"
done
sudo systemctl enable --now iscsid
```

On macOS, a separate Lima VM can provide this Linux host:

```sh
limactl start --name=lvmo-kind-pod --cpus=4 --memory=6 --disk=40 \
  --mount-none --tty=false template:docker-rootful
limactl shell lvmo-kind-pod
```

Install the prerequisites and clone the repository inside that VM. The remaining
commands execute there, with Docker access. Do not start a standalone host NFS
server or lvmo API alongside the Pod server.

## Create the cluster and install lvmo

```sh
sudo bash scripts/quickstart-kind.sh
```

The script builds a custom [Kind node image](../examples/kind/Dockerfile) with
NFS/iSCSI userspace tools and creates `lvmo-pod` using the pinned Kubernetes
1.35.0 image. It builds and loads both local lvmo images, installs the snapshot
controller, and installs the Helm chart into `lvmo-system`.

The [Kind configuration](../examples/kind/cluster.yaml) supplies host kernel
modules and the host-process reference used by lvmo's existing nested-iSCSI
helper. It also binds a real host filesystem at the local-path provisioner's
storage directory. This avoids placing the backing PVC on a container overlay.
The host iSCSI daemon is shared; this is not an independent-initiator test.

The generated `.test/kind/values.yaml` enables the server, requests a 5Gi source
PVC on `standard`, pins the server to `lvmo-pod-control-plane`, and restricts NFS
to that node's address. The loop image uses only part of the requested capacity,
leaving space for filesystem and thin-pool metadata. Local-path storage does not
enforce a PVC size quota, so the server sizes the image from the requested value.

```sh
sudo kubectl --context kind-lvmo-pod -n lvmo-system get pods,pvc
sudo kubectl --context kind-lvmo-pod get sc lvmo-test-sc-iscsi lvmo-test-sc-nfs
```

## Try both protocols

```sh
sudo kubectl --context kind-lvmo-pod apply -f examples/kind/workloads.yaml
sudo kubectl --context kind-lvmo-pod -n lvmo-demo wait pod --all \
  --for=condition=Ready --timeout=180s
for protocol in iscsi nfs; do
  sudo kubectl --context kind-lvmo-pod -n lvmo-demo exec "client-$protocol" -- \
    sh -c "echo lvmo-$protocol > /data/proof; sync; cat /data/proof"
done
```

The commands must print `lvmo-iscsi` and `lvmo-nfs` respectively. The NFS claim uses RWX Filesystem mode;
the iSCSI claim uses RWO Filesystem mode. These are application volumes created
inside the loop-backed pool, separate from its source PVC.

## Upgrade and remove

Keep the saved values explicit on upgrades:

```sh
sudo helm upgrade lvmo charts/lvmo-csi --kube-context kind-lvmo-pod \
  -n lvmo-system -f .test/kind/values.yaml --wait
```

An omitted switch does not always mean disabled: Helm's reuse/reset flags
control the resulting values. Disabling the server or uninstalling with live
volumes must be refused by the lifecycle guard. Do not bypass the hooks.
A refused uninstall can leave Helm's status as `uninstalling`; clean up the
consumers and retry uninstall rather than attempting an ordinary upgrade.

```sh
sudo kubectl --context kind-lvmo-pod delete namespace lvmo-demo
# Wait until consumer PVs and snapshots are reclaimed; only the source PVC remains.
sudo kubectl --context kind-lvmo-pod get pv
sudo helm uninstall lvmo --kube-context kind-lvmo-pod -n lvmo-system --wait
# The source PVC is deliberately retained. Delete it explicitly after checking cleanup.
sudo kubectl --context kind-lvmo-pod -n lvmo-system delete pvc lvmo-storage
sudo kind delete cluster --name lvmo-pod
```

Keep the Linux VM for reuse, or delete it explicitly with
`limactl delete lvmo-kind-pod` on the Mac once its data is no longer needed.
