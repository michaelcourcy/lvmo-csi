# Pod-based test storage server

> **Prerelease:** the two-chart installation below uses `v0.2.0-alpha.1`.
> Older combined charts still use `create-storage-server`.

Install one `lvmo/lvmo-csi` driver release per cluster and independently install
one or more `lvmo/lvmo-csi-storage-server` releases. The containerized server is
for functional testing, without replication or automatic failover. It uses an
existing PVC (Filesystem, or Block in [block mode](#block-mode)) as backing storage and does not improve its durability
or performance. External Linux storage servers continue to work without the
storage-server chart.

Both charts are published in the existing GitHub Pages Helm repository,
with the same chart version and application version for each release. For tag
`vX.Y.Z`, both chart versions are `X.Y.Z` and both appVersions are `vX.Y.Z`;
prerelease suffixes are preserved. Each chart's `image.repository`, `image.tag`
and `image.pullPolicy` configure its own image. An empty tag uses appVersion.
The Docker Hub repositories remain `michaelcourcy/lvmo-csi` and
`michaelcourcy/lvmo-csi-storage-server`.

## Installation and values

Install the snapshot CRDs/controller separately; snapshot-class creation is
enabled by default. Install the driver first, then a server (replace the source class):

```sh
helm repo add lvmo https://michaelcourcy.github.io/lvmo-csi
helm repo update
helm upgrade --install lvmo lvmo/lvmo-csi --version 0.2.0-alpha.1 \
  --namespace lvmo-csi --create-namespace
helm upgrade --install server-a lvmo/lvmo-csi-storage-server --version 0.2.0-alpha.1 \
  --namespace lvmo-csi --create-namespace \
  --set source-storage-class=my-local-storageclass
```

These are independent releases, with no Helm dependency between the charts.
There is no server `enabled` switch: installing or uninstalling its release
controls its lifecycle. The driver no longer accepts `create-storage-server`;
legacy values should produce a clear error instead of silently doing nothing.
Migration of existing combined installations is outside this change. Clean up
old test consumers and snapshots with the old installation before uninstalling
it and explicitly deleting its retained backing PVC for a fresh installation.

| Chart | Value | Default / meaning |
|---|---|---|
| Driver | `snapshotClass.enabled` | `true`; create the shared class unless explicitly disabled |
| Driver | `snapshotClass.name` | `lvmo-snapshots` |
| Driver | `snapshotClass.deletionPolicy` | `Delete`; also accepts `Retain` |
| Driver | `snapshotClass.kasten` | `true`; add `k10.kasten.io/is-snapshot-class: "true"`; set false to omit |
| Server | `source-storage-class` | Required existing non-lvmo class; Filesystem, or Block in block mode |
| Server | `size` | `50Gi` |
| Server | `block-mode` | `false`; `true` uses a Block-mode PVC as the LVM physical volume, see [Block mode](#block-mode) |
| Server | `state-size` | `1Gi`; size of the Filesystem state PVC in block mode |
| Server | `dest-storage-class-prefix` | Empty means the server release name; explicit override allowed |
| Server | `resources` | `{}`; no chart-supplied CPU/memory requests or limits |

When creation is enabled, install/upgrade must check that the
`snapshot.storage.k8s.io/v1` VolumeSnapshotClass API is available. If it is
missing, fail before applying chart resources with an actionable message:

> Snapshot CRDs are missing. Install the snapshot CRDs and controller following
> https://github.com/kubernetes-csi/external-snapshotter/tree/v8.5.0#usage
> or install lvmo with `--set snapshotClass.enabled=false` to skip creating the
> VolumeSnapshotClass. Snapshot operations still require the CRDs/controller.

The [upstream installation instructions](https://github.com/kubernetes-csi/external-snapshotter/tree/v8.5.0#usage)
cover both components. A registered API does not prove that the controller is
running; controller readiness remains a prerequisite for snapshot operations.
Offline `helm template` checks that expect snapshot output must advertise
`--api-versions snapshot.storage.k8s.io/v1/VolumeSnapshotClass`; without that
capability, default rendering must show the same explicit error. Rendering
with `--set snapshotClass.enabled=false` must work without that capability.

Server resource requests and limits are optional and independently configurable
through standard Kubernetes `resources` values. For example:

```yaml
resources:
  requests:
    cpu: 100m
    memory: 256Mi
  limits:
    cpu: "2"
    memory: 2Gi
```

With the default `{}`, the chart sets neither requests nor limits on the server
container. Namespace LimitRanges or admission policies may still add defaults
or require explicit values. Resource configuration does not change the backing
PVC's capacity. The 5Gi sizes in test scenarios are explicit test overrides;
the chart default is 50Gi.

Only the driver chart creates the optional shared VolumeSnapshotClass. It is
cluster-scoped, selects `lvmo.csi.io`, and contains no server endpoint or VG.
The source volume handle routes snapshot creation to its backend. One class
works for all servers and both protocols; restore targets must still use the
source backend. Cross-server LVM cloning is not supported. Additional classes
are useful for different policies, such as `Retain`, not for different servers.
Use an existing class with `snapshotClass.enabled=false` when it is managed
elsewhere. Configure at most one Kasten-preferred class for `lvmo.csi.io`.
No Kubernetes default-snapshot-class annotation is implied by the Kasten option.

The driver retains its current driver, sidecar, OpenShift and metadata values.
Server settings move out of `create-storage-server` to the server chart root.
Use Kubernetes quantities: `5G` is decimal and `5Gi` binary; `5GB` is invalid.
The effective destination prefix must be a valid DNS label of at most 55
characters; invalid values fail rather than being silently truncated.

## Namespace layouts and multiple servers

The server may share the driver namespace, use another namespace, or share a
separate namespace with other servers. For example, after installing the driver
in `lvmo-csi`, install two servers in `lvmo-storage`:

```sh
for server in server-a server-b; do
  helm upgrade --install "$server" lvmo/lvmo-csi-storage-server --version 0.2.0-alpha.1 \
    --namespace lvmo-storage --create-namespace \
    --set source-storage-class=my-local-storageclass
done
```

This creates classes `server-a-nfs`, `server-a-iscsi`, `server-b-nfs` and
`server-b-iscsi`. Endpoints are `server-a-storage.lvmo-storage.svc:50051` and
`server-b-storage.lvmo-storage.svc:50051`. Each release owns its server, Service
and backing PVC named `<release>-storage`, and a VG derived from namespace and
release name. StorageClass names are cluster-wide: reuse of a release name in
another namespace requires a distinct `dest-storage-class-prefix`. Collisions
must fail without adopting another release's resources.

Network policy and routing must permit management access from the driver and
NFS/iSCSI access from client nodes. Namespace placement does not provide storage
isolation or relax the host privileges and kernel prerequisites below.

For development, use `charts/lvmo-csi` and
`charts/lvmo-csi-storage-server`, overriding each chart's own `image.repository`
and `image.tag` with builds in the environment's permitted registry. For
released charts, pin the same `--version` for both initially; independent Helm
upgrades do not imply compatibility between arbitrary application versions.
Without a version Helm selects stable releases; use `--devel` for prereleases.

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
  rather than hiding source exhaustion behind a sparse file. The loop device
  uses direct I/O (`O_DIRECT`) on the backing file, so blocks are not cached a
  second time in the node's page cache and flushes reach the source disk
  without buffered writeback. If the source filesystem does not support direct
  I/O, the server logs it and stays in buffered mode; the startup log records
  the mode (`direct I/O: 1` or `0`).
- One IPv4 ClusterIP Service on TCP ports 50051 (API), 2049 (NFSv4) and 3260
  (iSCSI). Generated StorageClasses use `<release>-storage.<namespace>.svc:50051`.
  The server resolves that Service to its ClusterIP for NFS/iSCSI volume metadata;
  client nodes must be able to reach the Service network.
- Cluster-scoped StorageClasses `<prefix>-iscsi` and `<prefix>-nfs`, both
  using `lvmo.csi.io`, the same API endpoint/VG, and their respective protocols.
  They are not default classes. Name collisions must fail rather than adopting
  an unrelated class. The iSCSI class carries the annotation
  `k10.kasten.io/sc-supports-block-mode-exports: "true"`, so Kasten can export
  its volumes in block mode; a Filesystem PVC still opts in with
  `k10.kasten.io/pvc-export-volume-in-block-mode`. The NFS class does not: in
  Kasten 9.0.7, a block export cannot be restored to NFS
  ([scenario](../tests/scenarios/kasten-block-mode-export.md)).
- No VolumeSnapshotClass or CSI driver resources in the server release. The
  shared class belongs to the driver release or is managed externally.

The source PVC supplies bytes; lvmo supplies thin snapshots and the NFS/iSCSI
interfaces. Applications use the generated classes normally. Client nodes still
need the NFS and iSCSI prerequisites described in [nodes.md](nodes.md).

Since `v0.1.0-alpha.5`, the pod storage server uses API port `50051`, matching
the standalone server. Release `v0.1.0-alpha.4` still uses `50061`; use a
matching chart and server image built from the same version. Existing StorageClass parameters and volume
handles retain the old endpoint, so this port change is not an in-place upgrade
for an installation with existing volumes. For a disposable test installation,
clean up its consumers, snapshots and volumes using the old release before
uninstalling and reinstalling with the new chart and image.

## Block mode

By default the VG lives in a loop-attached file on a Filesystem PVC, which
works with any class, including Kind's and minikube's. When the source class
supports Block volumes, `--set block-mode=true` removes the loop device and the
source filesystem:

- PVC `<release>-storage` is created with `volumeMode: Block`. The server
  mounts the host's `/dev`, and containerd then does not create the container's
  `volumeDevices` node. So an init container without that mount receives the
  device and records its major:minor, and the server recreates it at
  `/lvmo-dev/backing`, outside `/dev`. The server creates the PV and VG directly
  on it; the thin pool takes about 90% of the device, with no 20% filesystem
  reserve.
- A second PVC, `<release>-storage-state`, of `state-size` and the same class
  in Filesystem mode, holds the API state, identity and lock at `/backing`. The
  uninstall guard mounts it read-only, as it mounts the single PVC in loop mode.
- The VG is created with auto-activation disabled, so a node whose OS runs
  LVM event activation does not activate it when the device reattaches.

The source class must bind `WaitForFirstConsumer`. Two zonal PVCs bound
`Immediate` could land in different zones, leaving the server unschedulable;
install and upgrade refuse such a class with an explicit error. The check uses
a cluster lookup, so plain `helm template` cannot apply it. A class without
Block support leaves `<release>-storage` Pending; use loop mode there.

`block-mode` and `state-size` are part of the backend identity: an upgrade that
changes either is rejected. Both PVCs are retained on uninstall.

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

Server Helm uninstall is guarded by a pre-delete inspection Job. It uses
required Pod affinity to run on the server's node and mounts its RWO backing
PVC read-only. Refuse uninstall if volumes, snapshots or pending reclamation
remain, or if inspection fails. Keep the server running after refusal so
consumers can be cleaned up. A failed pre-delete hook can leave the release
`uninstalling`; clean up consumers, then retry uninstall. Hooks do not protect
against `--no-hooks` or direct Kubernetes deletion.

There is no disable/reset-to-disabled lifecycle in the separate server chart.
Upgrades with explicit saved values or `--reuse-values` must preserve backend
identity and data. Reject changes to source class, size, prefix or address
instead of silently reinitializing storage. Driver upgrades must not change
server resources; server upgrades must not change driver resources or the
shared snapshot class. Retaining a PVC alone does not guarantee reinstall
with a newly allocated Service IP can reuse existing backend state.

Stop new consumer creation during cleanup. Delete consumers and snapshots while
both driver and servers are running, wait for backend reclamation, and uninstall
each server. Its classes, Service and workload are removed; its source PVC is
retained for explicit deletion. Other servers and the shared snapshot class
remain. Uninstall the driver last, and only if it is owned by this run and no
other backend needs it. Independent releases do not prevent an operator from
uninstalling the driver prematurely. A driver-owned snapshot class is removed
with its release; existing snapshot cleanup must therefore be completed first.

## Removal policy

Server uninstall requires successful backend verification: the guard checks TCP
reachability and the persisted `/backing/state/state.json` volume, snapshot and
pending-deletion records. It refuses removal while any of those records remain
or inspection fails. It does not execute `lvs` or query Kubernetes PVC/PV objects.
The source PVC remains retained for explicit deletion after successful uninstall.

Deleting PVCs, PVs, VolumeSnapshots or VolumeSnapshotContents, particularly by
removing finalizers, does not prove backend reclamation. Retain policies can
also deliberately leave backend data. The absence of Kubernetes objects is
therefore insufficient to permit server uninstall; orphaned backend records
still block removal. See
[Kubernetes finalizer semantics](https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/).

A Kubernetes inventory may be added later to improve diagnostics, but cannot
replace backend verification. Such an inventory would need to include retained
PVs and VolumeSnapshotContents, resolve each object's backend rather than rely
only on class names, and exclude the server's source PVC/PV.

Driver uninstall remains independent of storage-server releases. It removes
the driver-owned snapshot class with the driver release, without checking how
many server releases exist. Externally managed snapshot classes are unaffected.
Counting Helm server releases would miss externally managed storage servers,
and retaining the class alone would not preserve snapshot operations.

Already mounted NFS/iSCSI volumes may continue serving data from the storage
server after driver removal, but new mounts, provisioning, expansion, snapshot
operations and CSI cleanup require the driver. Complete consumer and snapshot
cleanup while the driver is running, and uninstall it last when no backend
needs it. Continued I/O on existing mounts is not a guarantee of normal workload
operation after driver removal.

## Acceptance

Namespace layouts, independent releases and shared snapshots are specified in
[independent-helm-charts](../tests/scenarios/independent-helm-charts.md).
Server lifecycle validation follows [pod-storage-server](../tests/scenarios/pod-storage-server.md)
and the separate [Kind quickstart](quickstart-kind.md). Chart rendering alone cannot
validate kernel service startup, cross-node access or restart cleanup.

Both independent-chart and server-lifecycle scenarios passed on a fully
recreated EKS cluster with EBS gp3 backing on 10 October 2026. The run covered
same/different namespaces, three servers sharing one snapshot class, both
protocols and snapshot restores, independent upgrades/removal, server
replacement and guarded uninstall. Mounted clients recovered in 1.4 seconds
for iSCSI and 89 seconds for NFS. This does not validate the Kind quickstart or
public chart publication.

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

The previous combined-chart Pod-network/Service implementation passed the updated scenario on EKS with
EBS gp3 on 9 October 2026: cross-node NFS/iSCSI, both snapshot restores, changed
Pod IPs behind an unchanged Service, and the full upgrade/disable/uninstall
sequence. Existing mounted consumers recovered after replacement in 3 seconds
for iSCSI and 92 seconds for NFS, measured from API readiness, with hashes and
new writes verified. Startup requires D-Bus before the first targetcli command.

The previous host-network implementation was validated on Kind with local-path
on 8 October 2026. The updated Kind networking/host-route path still needs its
own runtime validation. These tests do not establish support for every source
filesystem, NFS lock/delegation reclaim, or abrupt node loss.
