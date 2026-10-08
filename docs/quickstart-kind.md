# Quickstart: lvmo entirely inside Kind

The previous host-network version was validated on 8 October 2026 in an Ubuntu
24.04 ARM64 Lima VM. The Pod-network/Service version awaits runtime validation.
This guide runs
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
PVC on `standard`, and publishes the server through a ClusterIP Service. No node
name or IP values are required. The local-path PV supplies node affinity. NFS
exports use `*`; keep this test cluster reachable only by trusted clients. The loop image uses only part of the requested capacity,
leaving space for filesystem and thin-pool metadata. Local-path storage does not
enforce a PVC size quota, so the server sizes the image from the requested value.

The script also adds a host route for the storage Service's single IPv4 address
through the Kind node. The nested iSCSI helper uses the Linux host's `iscsid`,
which otherwise cannot reach the cluster's Service network. Remove that route
as shown below during cleanup. Ordinary Kubernetes workers do not need it.

The creation script runs as root. Export the cluster context to your user's
kubeconfig before running the following `kubectl` and `helm` commands without
`sudo`:

```sh
mkdir -p ~/.kube
sudo kind export kubeconfig --name lvmo-pod --kubeconfig "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" ~/.kube/config
kubectl config use-context kind-lvmo-pod

kubectl -n lvmo-system get pods,pvc
kubectl get sc lvmo-test-sc-iscsi lvmo-test-sc-nfs
```

For Bash, enable completion for `kubectl` and its `k` alias in the current shell:

```sh
sudo apt-get install -y bash-completion
source /usr/share/bash-completion/bash_completion
source <(kubectl completion bash)
alias k=kubectl
complete -o default -F __start_kubectl k
```

To keep this configuration in future Bash sessions, add the two `source` lines,
the `alias` line, and the `complete` line to `~/.bashrc`.

## Try both protocols

```sh
kubectl apply -f examples/kind/workloads.yaml
kubectl -n lvmo-demo wait pod --all \
  --for=condition=Ready --timeout=180s
for protocol in iscsi nfs; do
  kubectl -n lvmo-demo exec "client-$protocol" -- \
    sh -c "echo lvmo-$protocol > /data/proof; sync; cat /data/proof"
done
```

The commands must print `lvmo-iscsi` and `lvmo-nfs` respectively. The NFS claim uses RWX Filesystem mode;
the iSCSI claim uses RWO Filesystem mode. These are application volumes created
inside the loop-backed pool, separate from its source PVC.

## Upgrade and remove

Keep the saved values explicit on upgrades:

```sh
helm upgrade lvmo charts/lvmo-csi \
  -n lvmo-system -f .test/kind/values.yaml --wait
```

An omitted switch does not always mean disabled: Helm's reuse/reset flags
control the resulting values. Disabling the server or uninstalling with live
volumes must be refused by the lifecycle guard. Do not bypass the hooks.
A refused uninstall can leave Helm's status as `uninstalling`; clean up the
consumers and retry uninstall rather than attempting an ordinary upgrade.

```sh
kubectl delete namespace lvmo-demo
# Wait until consumer PVs and snapshots are reclaimed; only the source PVC remains.
kubectl get pv
# Save the Service address before Helm removes it.
STORAGE_SERVICE_IP=$(kubectl -n lvmo-system get svc lvmo-storage -o jsonpath='{.spec.clusterIP}')
helm uninstall lvmo -n lvmo-system --wait
sudo ip route del "$STORAGE_SERVICE_IP/32"
# The source PVC is deliberately retained. Delete it explicitly after checking cleanup.
kubectl -n lvmo-system delete pvc lvmo-storage
sudo kind delete cluster --name lvmo-pod
```

Keep the Linux VM for reuse, or delete it explicitly with

```sh
limactl stop lvmo-kind-pod
limactl delete lvmo-kind-pod
```

on the Mac once its data is no longer needed.
