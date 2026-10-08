# Pod-based test storage server

This opt-in feature is for testing. It packages a single
lvmo storage server in the CSI release namespace, using an existing Filesystem
PVC as backing storage. It is for functional testing, without replication or
automatic failover. It does not improve the durability or performance of the
source storage.

Build `Dockerfile.storage-server` into the test environment's registry. Set
`TEST_NODE` and `TEST_SERVER_IP` to the dedicated worker name/address,
`TEST_CLIENT_IP` to the allowed NFS client address (or an explicitly scoped CIDR),
and `TEST_REGISTRY`/`TEST_SERVER_TAG` to that image. Install with:

```sh
helm upgrade --install lvmo charts/lvmo-csi --namespace lvmo-csi --create-namespace \
  --set create-storage-server.enabled=true \
  --set create-storage-server.source-storage-class=my-local-storageclass \
  --set create-storage-server.size=5Gi \
  --set create-storage-server.dest-storage-class-prefix=lvmo-test-sc \
  --set create-storage-server.node-name="$TEST_NODE" \
  --set create-storage-server.server-address="$TEST_SERVER_IP" \
  --set create-storage-server.nfs-clients="$TEST_CLIENT_IP" \
  --set create-storage-server.image.repository="$TEST_REGISTRY/lvmo-csi" \
  --set create-storage-server.image.tag="$TEST_SERVER_TAG"
```

Use Kubernetes quantities: `5G` means decimal gigabytes and `5Gi` means binary
gibibytes; `5GB` is not a valid PVC quantity. The option defaults to disabled.

## Resources and data path

- One source RWO Filesystem PVC in the release namespace, requesting `size` from
  the explicitly named existing StorageClass. It must not use lvmo itself or one
  of the generated classes, which would create a provisioning dependency cycle.
- One privileged storage-server workload, with one replica and no overlapping
  replacement. It is pinned to one eligible Linux
  node; an exclusive backing-file lock prevents overlapping owners. A local source PV's node affinity must agree with that placement.
- A regular file on the source PVC, attached to a loop device, holding a private
  LVM VG and `lvmo-pool` thin pool. Reserve space for the source filesystem,
  persistent API metadata and thin-pool metadata; usable capacity is smaller
  than the requested PVC size. Allocate the backing file before serving volumes
  rather than hiding source exhaustion behind a sparse file.
- A stable API endpoint selected by the generated StorageClasses, plus a stable
  NFS/iSCSI address reachable from every client node.
- Cluster-scoped StorageClasses `lvmo-test-sc-iscsi` and `lvmo-test-sc-nfs`, both
  using `lvmo.csi.io`, the same API endpoint/VG, and their respective protocols.
  They are not default classes. Name collisions must fail rather than adopting
  an unrelated class.

The source PVC supplies bytes; lvmo supplies thin snapshots and the NFS/iSCSI
interfaces. Applications use the generated classes normally. Client nodes still
need the NFS and iSCSI prerequisites described in [nodes.md](nodes.md).

## Host integration and image

The driver image does not contain the storage API or its server tools.
Build `Dockerfile.storage-server` for the separate storage-server image with `lvmo-csi`, LVM/thin tools, loop utilities,
filesystem tools, kernel NFS userspace tools and targetcli.

Kernel NFS, LIO iSCSI targets, device mapper and loop devices need privileged
access. This implementation uses a dedicated test node, host networking
and an explicitly selected node address. Do not assume that an ordinary Pod IP
or an iSCSI Service is sufficient: iSCSI discovery advertises target portals,
and the backend persists the advertised server address with each volume.
Validate discovery and reconnect from another node. Ports 2049, 3260 and 50061
must be available; use NFSv4 to avoid an additional NFSv3 port-mapping surface.

The entrypoint starts services without systemd and exits if the API or mountd exits. Restrict LVM
discovery to the owned loop device. Never scan, initialize or deactivate other
host disks/VGs, flush all NFS exports, or clear unrelated iSCSI targets. Host
kernel support and a source filesystem suitable for a loop-backed image are
preconditions; this cannot work on every managed Kubernetes platform.

## Restart and lifecycle

Persist the backing file and API state together on the source PVC. On restart,
reuse the existing loop attachment if present, activate the existing VG and
reconcile exports/targets. Never truncate an existing image or recreate its VG.
The existing API state lock alone does not protect loop/VG initialization:
acquire exclusive ownership before either operation.

Pinning avoids treating node relocation as safe failover. Do not force-delete
the server Pod to start another copy while the original node may still serve
storage. Recovery after loss of the node is outside the first implementation.

Graceful shutdown must stop the API, withdraw only owned exports/targets,
unmount owned volumes, deactivate the owned VG and detach its loop device.
Restart also needs reconciliation of resources left by an abrupt termination.
Deleting a Pod does not by itself clean up host kernel objects.

Helm removal is guarded. A pre-upgrade hook is rendered when an existing server
is found, even when the new values disable it; a conditional hook inside the enabled
block would disappear precisely when it is needed. A pre-delete hook performs
the same check before uninstall. Refuse removal if volumes, snapshots or pending
backend reclamation remain, or if the existing server cannot be inspected. Keep
the server and CSI driver running after a refusal so consumers can be cleaned up.
A failed pre-delete hook can leave Helm's release status as `uninstalling` even
though serving resources remain. Clean up consumers, then retry uninstall; do
not promise that an ordinary upgrade is available in that intermediate state.
Hooks are protection for normal Helm commands, not protection against deliberate
bypass with `--no-hooks` or direct Kubernetes deletion.

Omitting the option is not an explicit disable: Helm value reuse/reset flags
control the resulting value. Use a saved values file for repeatable upgrades.
Stop creating new consumer PVCs/snapshots during removal; hooks do not serialize
concurrent provisioning by other Kubernetes clients. Test `--reuse-values` omission (server remains enabled) and `--reset-values`
omission (default false, guarded removal), as well as explicit `enabled=false`.
An unchanged enabled upgrade must preserve storage even while consumers exist.

Retain the source PVC on Helm uninstall or disable by default. Do not retain the
privileged server workload with Helm's keep annotation. Delete consumer PVCs and
snapshots first, verify backend cleanup, then uninstall and explicitly remove
the retained source PVC when its data is no longer needed. Keeping the PVC does
not make changing source class, VG identity, node address or size a supported
upgrade; reject unsupported changes instead of silently reinitializing data.

## Acceptance

Validation follows [pod-storage-server](../tests/scenarios/pod-storage-server.md)
and the separate [Kind quickstart](quickstart-kind.md). Chart rendering alone cannot
validate kernel service startup, cross-node access or restart cleanup.

## Container NFS setup

A local ext4 mount on the server is still required for every NFS volume. The
client separately mounts that exported filesystem with NFSv4.1. In the Pod,
`/etc/mtab` links to `/proc/mounts`: without it Ubuntu mountd crashed while
reading the mount table. That fix alone did not solve the container root path.

The server exports `/backing/nfs-root` read-only with `fsid=0`, restricted by
`nfs-clients`. Only the volume directory is mirrored beneath it using a shared
bind mount confined to the Pod's private mount namespace. The image's exportfs
adapter publishes each mirrored child with its own writable policy and fsid.
Neither `disk.img` nor `state.json` is inside the exported root. Root access
remains squashed on the root; application exports retain lvmo's existing policy.

The [reference Kubernetes manifest](https://github.com/kastendevhub/enterprise-blueprint/blob/main/generic-rwx-storage/nfs-server.yaml)
uses `itsthenetwork/nfs-server-alpine`; its startup script explicitly exports a
single filesystem as `fsid=0`. lvmo needs separate child exports for dynamically
mounted LVs. No NFS client changes were required on EKS.

The nested Kind iSCSI helper accesses the host device and its sysfs timeout
through the host process root. It must preserve the kernel's `/proc/.../root`
magic-link semantics rather than resolve that path against the container root.

Validated on 8 October 2026 with EBS gp3 on EKS and local-path on Kind in a
Linux VM. EKS coverage includes snapshots, server replacement and the full
upgrade/disable/uninstall sequence. Kind coverage includes the documented
installation, both protocols, consumer restart and guarded uninstall. This does
not establish support for every source filesystem or for abrupt node loss.
