# Pod-based test storage server

This opt-in feature is for testing. It packages a single
lvmo storage server in the CSI release namespace, using an existing Filesystem
PVC as backing storage. It is for functional testing, without replication or
automatic failover. It does not improve the durability or performance of the
source storage.

The release workflow publishes `michaelcourcy/lvmo-csi-storage-server:<version>`
for Linux AMD64 and ARM64 alongside `michaelcourcy/lvmo-csi:<version>`, using
the same `v*` tag. This applies to releases made with the updated workflow;
older releases do not automatically gain a storage-server image or these defaults.
The packaged chart defaults to both Docker Hub repositories and uses its
`appVersion` for both image tags. For release `vX.Y.Z`, the chart version is
`X.Y.Z` and `appVersion` is `vX.Y.Z` (prerelease suffixes are preserved).

Set `VERSION` to a published `v*` tag built with the updated workflow. Install
its chart directly from the GitHub release, replacing the source StorageClass
with one available in your cluster:

```sh
helm upgrade --install lvmo \
  "https://github.com/michaelcourcy/lvmo-csi/releases/download/${VERSION}/lvmo-csi-${VERSION#v}.tgz" \
  --namespace lvmo-csi --create-namespace \
  --set create-storage-server.enabled=true \
  --set create-storage-server.source-storage-class=my-local-storageclass \
  --set create-storage-server.size=5Gi \
  --set create-storage-server.dest-storage-class-prefix=lvmo-test-sc
```

The chart package selects the application version; the Helm release name `lvmo`
does not. No image overrides, node name, server IP or NFS client IP are required.
On upgrades, remove old explicit image overrides from your saved values if you
want to follow the chart's defaults; `--reuse-values` can retain those overrides.

For development, build both Dockerfiles into the environment's permitted
registry and install from `charts/lvmo-csi`. The source chart uses `appVersion: dev`; explicitly override both images with
your development builds:

```sh
--set image.repository="$TEST_REGISTRY/lvmo-csi" \
--set image.tag="$TEST_DRIVER_TAG" \
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
  replacement. Kubernetes schedules it on an eligible Linux node according to
  the source PV's topology; an exclusive backing-file lock prevents overlapping
  owners. A local source PV keeps replacements on its node.
- A regular file on the source PVC, attached to a loop device, holding a private
  LVM VG and `lvmo-pool` thin pool. Reserve space for the source filesystem,
  persistent API metadata and thin-pool metadata; usable capacity is smaller
  than the requested PVC size. Allocate the backing file before serving volumes
  rather than hiding source exhaustion behind a sparse file.
- One IPv4 ClusterIP Service on TCP ports 50051 (API), 2049 (NFSv4) and 3260
  (iSCSI). Generated StorageClasses use `<release>-storage.<namespace>.svc:50051`.
  The server resolves that Service to its ClusterIP for NFS/iSCSI volume metadata;
  client nodes must be able to reach the Service network.
- Cluster-scoped StorageClasses `lvmo-test-sc-iscsi` and `lvmo-test-sc-nfs`, both
  using `lvmo.csi.io`, the same API endpoint/VG, and their respective protocols.
  They are not default classes. Name collisions must fail rather than adopting
  an unrelated class.

The source PVC supplies bytes; lvmo supplies thin snapshots and the NFS/iSCSI
interfaces. Applications use the generated classes normally. Client nodes still
need the NFS and iSCSI prerequisites described in [nodes.md](nodes.md).

The source chart now uses API port `50051`, matching the standalone server.
Release `v0.1.0-alpha.4` still uses `50061`; use a matching chart and server
image built from the same version. Existing StorageClass parameters and volume
handles retain the old endpoint, so this port change is not an in-place upgrade
for an installation with existing volumes. For a disposable test installation,
clean up its consumers, snapshots and volumes using the old release before
uninstalling and reinstalling with the new chart and image.

## Host integration and image

The driver image does not contain the storage API or its server tools.
The separate storage-server image is built from `Dockerfile.storage-server` with `lvmo-csi`, LVM/thin tools, loop utilities,
filesystem tools, kernel NFS userspace tools and targetcli.

Kernel NFS, LIO iSCSI targets, device mapper and loop devices need privileged
access. The server uses an IPv4 Pod network (`hostNetwork: false`), with a Service
selecting its single replica. The Pod IP can change on replacement; the Service
IP must remain unchanged while volumes exist. Do not delete/recreate that
Service with live volumes: startup rejects a changed address in existing state.

LIO listens on the Pod IP, with automatic wildcard portals disabled. The CSI
node driver creates an iSCSI node record using the API-provided IQN and Service
IP, without SendTargets discovery: raw discovery would advertise the Pod IP and
bypass the stable Service. Reconnection must use the Service IP as well. The
server rebuilds only its own persisted targets on startup to discard sockets
left in an old Pod network namespace. LIO configuration and block devices remain
host-wide despite Pod networking; use authorized test nodes with kernel support.

NFS exports use the client selector `*` inside the Pod. Pod networking is not an
access-control boundary: restrict reachability to trusted test clients through
the cluster's network controls. There is no new management API authentication.
Only NFSv4 is exposed; NFSv3 port mapping is not published by the Service.

The entrypoint starts services without systemd and exits if the API or mountd exits. Restrict LVM
discovery to the owned loop device. Never scan, initialize or deactivate other
host disks/VGs, flush all NFS exports, or clear unrelated iSCSI targets. Host
kernel support and a source filesystem suitable for a loop-backed image are
preconditions; this cannot work on every managed Kubernetes platform.

## Restart and lifecycle

An existing host-network installation cannot be upgraded in place to this
Service identity. Clean up its consumers and snapshots, uninstall it with the
old chart, and explicitly delete its retained backing PVC before a fresh test
installation. The chart rejects an enabled upgrade that changes backend identity.

Persist the backing file and API state together on the source PVC. On restart,
reuse the existing loop attachment if present, activate the existing VG and
reconcile exports/targets. Never truncate an existing image or recreate its VG.
The existing API state lock alone does not protect loop/VG initialization:
acquire exclusive ownership before either operation.

Source-PV topology controls placement; a Service does not provide storage
fencing or safe failover. Do not force-delete
the server Pod to start another copy while the original node may still serve
storage. Recovery after loss of the node is outside the first implementation.

Graceful shutdown must stop the API, withdraw only owned exports/targets,
unmount owned volumes, deactivate the owned VG and detach its loop device.
Restart also needs reconciliation of resources left by an abrupt termination.
Deleting a Pod does not by itself clean up host kernel objects.

Helm removal is guarded. A pre-upgrade hook is rendered when an existing server
is found, even when the new values disable it; a conditional hook inside the enabled
block would disappear precisely when it is needed. A pre-delete hook performs
the same check before uninstall. Inspection Jobs use required Pod affinity to run
on the server's node and mount its RWO backing PVC read-only. If no server can
be reached or the Job cannot be scheduled, removal fails closed. Refuse removal
if volumes, snapshots or pending backend reclamation remain, or if the existing server cannot be inspected. Keep
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
not make changing source class, VG identity, Service identity or size a supported
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

The server exports `/backing/nfs-root` read-only with `fsid=0` and client
selector `*`. Only the volume directory is mirrored beneath it using a shared
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

The Pod-network/Service implementation passed the updated scenario on EKS with
EBS gp3 on 9 October 2026: cross-node NFS/iSCSI, both snapshot restores, changed
Pod IPs behind an unchanged Service, and the full upgrade/disable/uninstall
sequence. Existing mounted consumers recovered after replacement in 3 seconds
for iSCSI and 92 seconds for NFS, measured from API readiness, with hashes and
new writes verified. Startup requires D-Bus before the first targetcli command.

The previous host-network implementation was validated on Kind with local-path
on 8 October 2026. The updated Kind networking/host-route path still needs its
own runtime validation. These tests do not establish support for every source
filesystem, NFS lock/delegation reclaim, or abrupt node loss.
