---
type: lima
can-provide: [kubernetes, storage-server, storage-server-reboot, nfs-client]
creation: agent              # agent | contributor-only
---

# lima: Kind on Docker Desktop with a separate Lima storage VM

## Shape

A released driver installed into **Kind on Docker Desktop**, with **Lima used only as the storage server**. Kubernetes pulls `michaelcourcy/lvmo-csi` from Docker Hub. Creating it needs neither Go nor a source checkout. It targets macOS on Apple Silicon.

```mermaid
flowchart LR
    K[Kind on Docker Desktop] -->|API TCP 50051| H[host.docker.internal]
    K -->|NFS TCP 2049| H
    H -->|Lima port forwarding| V[Lima storage VM: API + LVM + NFS]
```

This is the layout of the README walkthrough. It has not yet been validated end to end; the automated results used [lima-nested](lima-nested.md).

## Cannot provide

- `iscsi-client`: the Kind nodes' kernel belongs to Docker Desktop, not Lima. Installing `iscsid` on the storage VM does not give Kind an initiator.
- `multi-node`, `distinct-initiators`, `node-power-control`: all Kind nodes share one Linux kernel. Stopping a node container does not stop its mounts.
- `kubevirt`, `openshift`.

## Preflight

- `limactl list` shows the VM `Running`.
- `limactl shell "$LVMO_VM" sudo systemctl is-active lvmo-loop lvmo-api` prints `active` twice, and `limactl shell "$LVMO_VM" sudo lvs` shows `lvmo-pool` in `lvmo-data`.
- `docker exec "$LVMO_CLUSTER-control-plane" bash -c 'timeout 5 bash -c "echo > /dev/tcp/host.docker.internal/50051"'` succeeds.
- `kubectl -n lvmo-system get pods` shows the controller and node pods ready.

## Deploy a code change

Build the Linux ARM64 API binary with `make build`, copy it into the VM with `limactl copy`, install it as `/usr/local/bin/lvmo-csi` and restart `lvmo-api`. Build the driver image, load it with `kind load docker-image --name "$LVMO_CLUSTER"`, then `helm upgrade` with that tag and `image.pullPolicy=IfNotPresent`. No registry is needed.

## Create

### 1. Prepare your laptop and select a release

Install and start Docker Desktop. With Homebrew installed:

```sh
brew install lima kind kubectl helm
mkdir -p ~/lvmo-demo
cd ~/lvmo-demo
export LVMO_VERSION=v0.1.0-alpha.2
export LVMO_CHART_VERSION=0.1.0  # chart version listed in that release
export LVMO_RELEASE_URL="https://github.com/michaelcourcy/lvmo-csi/releases/download/$LVMO_VERSION"
export LVMO_VM=lvmo-storage
export LVMO_CLUSTER=lvmo-demo
export KUBECONFIG="$PWD/kubeconfig"

docker context use desktop-linux
docker info
limactl --version
kind version
docker pull "michaelcourcy/lvmo-csi:$LVMO_VERSION"
curl -fL "$LVMO_RELEASE_URL/lvmo-csi-$LVMO_CHART_VERSION.tgz" -o lvmo-csi.tgz
```

The image pull checks release availability before creating infrastructure; Kind will pull the same image from Docker Hub when Helm installs the driver. Public images do not require registry credentials. The dedicated `KUBECONFIG` keeps this environment separate from your usual clusters. Run subsequent commands in this same terminal unless instructed to enter the VM.

### 2. Create the storage VM

Create this Lima configuration on the laptop:

```sh
cat > storage.yaml <<'YAML'
vmType: vz
cpus: 2
memory: 4GiB
disk: 30GiB
images:
- location: https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-arm64.img
  arch: aarch64
mounts: []
containerd:
  system: false
  user: false
portForwards:
- guestPort: 50051
  hostPort: 50051
  hostIP: 127.0.0.1
  static: true
- guestPort: 2049
  hostPort: 2049
  hostIP: 127.0.0.1
  static: true
- guestPort: 3260
  hostPort: 3260
  hostIP: 127.0.0.1
  static: true
YAML
limactl start --name="$LVMO_VM" --tty=false storage.yaml
limactl shell "$LVMO_VM" sudo env LVMO_VERSION="$LVMO_VERSION" bash
```

**The next two steps run in this root shell inside Lima.** Kind remains on Docker Desktop; no Kubernetes components are installed in this VM.

Lima forwards the three ports onto the Mac's loopback interface. Kind reaches them through Docker Desktop's `host.docker.internal` address. Ports 50051, 2049 and 3260 must be free on the Mac. This uses [Lima port forwarding](https://lima-vm.io/docs/config/port/) and [Docker Desktop host access](https://docs.docker.com/desktop/features/networking/networking-how-tos/); the guest's default private IP is not used by Kind.

### 3. Install the storage API and prepare its pool

```sh
set -e
apt-get update
apt-get install -y lvm2 thin-provisioning-tools nfs-kernel-server targetcli-fb xfsprogs curl ca-certificates
modprobe dm_thin_pool
modprobe target_core_mod
modprobe iscsi_target_mod
systemctl enable --now nfs-server

mkdir -p /tmp/lvmo-release
cd /tmp/lvmo-release
LVMO_RELEASE_URL="https://github.com/michaelcourcy/lvmo-csi/releases/download/$LVMO_VERSION"
curl -fLO "$LVMO_RELEASE_URL/lvmo-csi-linux-arm64"
curl -fLO "$LVMO_RELEASE_URL/SHA256SUMS"
awk '$2 == "lvmo-csi-linux-arm64"' SHA256SUMS > api.sha256
test -s api.sha256
sha256sum --check api.sha256
install -m 0755 lvmo-csi-linux-arm64 /usr/local/bin/lvmo-csi

mkdir -p /var/lib/lvmo-disks
truncate -s 12G /var/lib/lvmo-disks/data.img
LVMO_LOOP=$(losetup --find --show /var/lib/lvmo-disks/data.img)
pvcreate "$LVMO_LOOP"
vgcreate lvmo-data "$LVMO_LOOP"
lvcreate --type thin-pool -L 10G --poolmetadatasize 128M -n lvmo-pool lvmo-data
lvs
```

The `lvcreate --type thin-pool` command above creates the shared pool, not a PVC volume. It allocates 10 GiB for data and 128 MiB for allocation metadata inside `lvmo-data`. The driver creates a separate thin LV for each PVC later; see [Why thin pools?](../../README.md#why-thin-pools).

The loop-backed disk is a demonstration disk created inside this VM. Run its creation commands once on the fresh VM. A real storage server should use a dedicated persistent disk or partition.

`losetup` attaches `data.img` to a loop device (`/dev/loopN`), a virtual block device whose contents are the file. LVM keeps all its metadata on the physical volume itself. That includes the PV UUID, the `lvmo-data` VG, the thin pool and every PVC's LV, so it all lives inside `data.img` and survives a reboot. The only thing lost on reboot is the loop attachment. Re-attaching the file is therefore enough to bring the VG back. Never run `pvcreate` or `vgcreate` again on an existing image: they would overwrite the metadata and lose every volume. The loop number may change between boots; LVM finds PVs by UUID, so that does not matter.

Install a unit that re-attaches the image at boot:

```sh
cat > /etc/systemd/system/lvmo-loop.service <<'UNIT'
[Unit]
Description=Attach LVMO loop-backed disk
After=local-fs.target
Before=lvmo-api.service target.service
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'losetup -j /var/lib/lvmo-disks/data.img | grep -q . || losetup --find /var/lib/lvmo-disks/data.img'
ExecStartPost=/usr/bin/udevadm settle
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now lvmo-loop
```

The unit only needs to attach the disk. At startup the API reconciles its saved state ([`Reconcile`](../../internal/backend/backend.go)): it checks that `lvmo-data/lvmo-pool` is a thin pool, activates every ready volume and snapshot LV, remounts and re-exports NFS volumes, and recreates iSCSI backstores, targets and LUNs. So no fstab entries, `vgchange` or `targetcli` restore steps are needed. But `Reconcile` exits with an error if the VG is not visible yet, and systemd stops restarting the API after a few quick failures. That is why the unit runs before `lvmo-api.service` and waits for `udevadm settle` until LVM has seen the PV. The step 4 API unit also declares `Requires=`/`After=lvmo-loop.service`. Running before `target.service` keeps LIO's own boot-time restore from failing on block backstores whose LVs do not exist yet; the API recreates them either way. The `losetup -j` check makes the unit safe to rerun because it never attaches the same file twice.

### 4. Start the API

Still inside Lima:

```sh
cat > /etc/systemd/system/lvmo-api.service <<'UNIT'
[Unit]
Description=LVMO storage API
Wants=network-online.target
Requires=lvmo-loop.service
After=network-online.target nfs-server.service lvmo-loop.service
[Service]
ExecStart=/usr/local/bin/lvmo-csi --server=host.docker.internal --nfs-insecure -P 50051 lvmo-data
Restart=on-failure
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now lvmo-api
systemctl --no-pager status lvmo-api
exit
```

**You are now back on the laptop.** `--server=host.docker.internal` is the data address the API returns to the CSI node plugin. It deliberately names the endpoint reachable from Docker Desktop, rather than the VM's private address. The StorageClass will separately specify the management API's port.

`--nfs-insecure` is needed for Lima's forwarded connections. Lima opens a new connection to the NFS server, which can use a source port above 1023. The default NFS `secure` policy rejects such ports; `insecure` permits them. This changes a source-port check, not encryption. See [NFS export options](https://www.man7.org/linux/man-pages/man5/exports.5.html). The flag makes the API include `insecure` in every NFS export it manages, including new PVCs, restored PVCs, and existing exports regenerated on API restart. Keep the Mac listeners bound to `127.0.0.1`; for directly connected storage servers, leave the flag disabled unless this source-port allowance is required.

### 5. Create Kind on Docker Desktop

```sh
kind create cluster --name "$LVMO_CLUSTER" --image kindest/node:v1.35.0 --wait 180s
kubectl get nodes

# Install NFS client tools in the Kind node.
docker exec "$LVMO_CLUSTER-control-plane" sh -c 'apt-get update && apt-get install -y nfs-common'
# Check the API route from that node before installing the driver.
docker exec "$LVMO_CLUSTER-control-plane" getent hosts host.docker.internal
docker exec "$LVMO_CLUSTER-control-plane" bash -c 'timeout 5 bash -c "echo > /dev/tcp/host.docker.internal/50051"'
```

The final command should exit successfully. If it fails, check `limactl list`, `limactl shell "$LVMO_VM" sudo systemctl status lvmo-api`, and local port conflicts before proceeding. NFS mounts also require NFS client support in the Docker Desktop Linux kernel; installing userspace tools cannot add a missing kernel feature.

### 6. Install snapshots and the released CSI driver

Run on the laptop:

```sh
export SNAPSHOTTER_VERSION=v8.5.0
for resource in volumesnapshotclasses volumesnapshotcontents volumesnapshots; do
  kubectl apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/$SNAPSHOTTER_VERSION/client/config/crd/snapshot.storage.k8s.io_$resource.yaml"
done
for manifest in rbac-snapshot-controller.yaml setup-snapshot-controller.yaml; do
  kubectl apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/$SNAPSHOTTER_VERSION/deploy/kubernetes/snapshot-controller/$manifest"
done
kubectl -n kube-system rollout status deployment/snapshot-controller --timeout=180s

kubectl create namespace lvmo-system
kubectl label namespace lvmo-system pod-security.kubernetes.io/enforce=privileged
helm upgrade --install lvmo ./lvmo-csi.tgz -n lvmo-system \
  --set image.repository=michaelcourcy/lvmo-csi \
  --set-string image.tag="$LVMO_VERSION" \
  --set image.pullPolicy=Always \
  --wait --timeout 5m
kubectl -n lvmo-system get pods
```

Leave `apiEndpoint` and `iscsiHostProc` unset. Each StorageClass selects its storage server; this layout does not use the nested-Kind iSCSI helper.

```sh
kubectl apply -f - <<'YAML'
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: lvmo-nfs
provisioner: lvmo.csi.io
allowVolumeExpansion: true
reclaimPolicy: Delete
parameters:
  endpoint: host.docker.internal:50051
  protocol: nfs
  vg: lvmo-data
---
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: lvmo-snapshots
driver: lvmo.csi.io
deletionPolicy: Delete
YAML
kubectl get sc lvmo-nfs -o yaml
```

For another storage VM, create another StorageClass pointing at its reachable API endpoint. With this forwarding layout, each additional VM needs distinct host ports and corresponding data-address configuration; production servers normally use their own directly reachable IPs or DNS names. Changing an existing StorageClass does not migrate existing volumes.

Finally, write the instance file `.test/environments/<name>.md` with `type: lima`, the VM name, the Kind cluster name, the kubeconfig path and the API endpoint `host.docker.internal:50051`.

## Delete

Delete test workloads first, while the driver and storage VM are still running, and wait for their PVs and VolumeSnapshotContents to disappear (`kubectl get pv`, `kubectl get volumesnapshotcontent`). NFS delegations can delay physical LV reclamation; inspect `lvs` and the API logs if needed.

Then remove the cluster and the VM. This destroys all data in this environment:

```sh
helm uninstall lvmo -n lvmo-system
kind delete cluster --name "$LVMO_CLUSTER"
limactl delete --force "$LVMO_VM"
unset KUBECONFIG
```

Remove the instance file `.test/environments/<name>.md`.
