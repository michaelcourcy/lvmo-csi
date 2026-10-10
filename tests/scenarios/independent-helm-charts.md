---
id: independent-helm-charts
status: manual
groups: [basic, routing]
requires: [kubernetes, nfs-client, iscsi-client]
automation: none
---

# Independent driver and server releases with a shared snapshot class

## Purpose

Validate the [chart split](../../docs/pod-storage-server.md): one driver
serves independently managed servers in shared or separate namespaces, and a
single VolumeSnapshotClass routes snapshots to each source backend.

## Preconditions

- A named instance allows installing one run-owned CSI driver, privileged test
  servers, and the required host kernel access. No existing driver installation
  may be replaced. Snapshot CRDs/controller and NFS/iSCSI clients are ready.
- An independent non-lvmo Filesystem source class can supply three 5Gi RWO PVCs.
  Record its name, provisioner, binding mode and topology. Nodes can reach the
  servers' Service network. Record host loop/VG/export/target baselines.
- Namespaces `split-csi`, `split-storage` and `split-consumers`, releases and
  generated class names below are unused. Use environment-authorized images.
- Local render checks are in `scripts/test-pod-storage-chart.sh` and run
  through `make lint`. Runtime steps below remain manual.

## Steps

1. Render both source charts and lint with a nonempty server
   `source-storage-class`. For successful offline driver renders, pass
   `--api-versions snapshot.storage.k8s.io/v1/VolumeSnapshotClass`; lint can use
   `--set snapshotClass.enabled=false` because it has no live API discovery.
   Driver output must contain no server resources. Server output must contain
   no CSI driver resources or VolumeSnapshotClass. Supply legacy
   `create-storage-server.enabled=true` to the driver: rendering must fail with
   guidance to install the server chart.
2. Render driver defaults with the advertised snapshot API: expect
   `lvmo-snapshots`, `driver: lvmo.csi.io`, `deletionPolicy: Delete`, no endpoint
   or VG parameters and `k10.kasten.io/is-snapshot-class: "true"`. Render with
   `snapshotClass.name=split-snapshots`, `snapshotClass.deletionPolicy=Retain`
   and `snapshotClass.kasten=false`; verify the name, policy and absent annotation.
   Render without the snapshot API capability and without overrides: it must
   fail explicitly naming missing snapshot CRDs, linking
   `https://github.com/kubernetes-csi/external-snapshotter/tree/v8.5.0#usage`
   and suggesting `--set snapshotClass.enabled=false`. With that opt-out and
   no API capability, rendering must succeed without a snapshot class.
   Verify an invalid policy (for example `Keep`) is rejected when enabled.
   Render server defaults with only `source-storage-class=external`: expect a
   50Gi source PVC and no server CPU/memory requests or limits. Render with
   `resources.requests.cpu=100m`, `resources.requests.memory=256Mi`,
   `resources.limits.cpu=2` and `resources.limits.memory=2Gi`; verify all four
   values reach the server container. Separately render requests-only and
   limits-only configurations, verifying no chart-supplied counterpart.
3. Install release `split-driver` from `charts/lvmo-csi` in `split-csi`, using
   `snapshotClass.enabled=true`, `snapshotClass.name=split-snapshots`,
   `snapshotClass.deletionPolicy=Delete` and `snapshotClass.kasten=true`.
   Allow 5 minutes for readiness. Record shared class UID and Helm ownership.
4. Install these releases from `charts/lvmo-csi-storage-server`, each with
   `source-storage-class=<selected-class>` and `size=5Gi`; leave the destination
   prefix unset to test its release-name default. Allow 10 minutes per server.

   | Release | Namespace | Expected classes | API endpoint |
   |---|---|---|---|
   | `split-local` | `split-csi` | `split-local-nfs`, `split-local-iscsi` | `split-local-storage.split-csi.svc:50051` |
   | `split-a` | `split-storage` | `split-a-nfs`, `split-a-iscsi` | `split-a-storage.split-storage.svc:50051` |
   | `split-b` | `split-storage` | `split-b-nfs`, `split-b-iscsi` | `split-b-storage.split-storage.svc:50051` |

   Check distinct backing PVCs, Services and VG names. Verify all classes use
   `lvmo.csi.io`, only iSCSI classes have the Kasten block-export annotation,
   and `split-snapshots` remains the only snapshot class created by this run.
5. Render an additional release `split-a` in namespace `split-other` with
   `dest-storage-class-prefix=split-other-a`; verify distinct class names,
   endpoint and VG identity without installing it. Render an invalid prefix
   and one of 56 characters: reject both. Attempt an actual install of release
   `split-collision` in `split-storage` with prefix `split-a`, using
   `--wait --timeout 2m`. It must fail rather than take ownership of existing
   classes. Record original class UIDs and ownership before/after. Inventory
   partial resources from the failed release and clean them up under Cleanup.
6. In `split-consumers`, create one 256Mi Filesystem PVC and consumer Pod for
   each of the six classes: NFS RWX and iSCSI RWO. Name claims and Pods
   `<release>-<protocol>`. Allow 5 minutes for each mount. Write a distinct 16Mi
   file per claim, sync it and record SHA-256, PV handle, endpoint and VG.
7. Create one VolumeSnapshot `<claim>-snap` per claim, all specifying
   `volumeSnapshotClassName: split-snapshots`. Allow 5 minutes for readyToUse.
   Record bound VolumeSnapshotContent handles and verify each routes to the
   source backend. Restore each into a 256Mi PVC `<claim>-restore` using the
   source StorageClass; mount and verify its hash within 5 minutes. Do not use
   a different backend as a restore destination.
8. Record driver and server Pod UIDs, backing PVC UIDs, VG UUIDs, Service IPs,
   class ownership and shared snapshot-class UID. Perform unchanged driver
   upgrade with saved values, then an unchanged upgrade of `split-a` with its
   saved values (5 minutes each). Driver upgrade must leave all server
   identities unchanged; `split-a` upgrade must leave driver and other server
   identities unchanged. Verify all original/restored hashes and class UID.
9. Delete only `split-a` consumer Pods, original/restore PVCs and snapshots;
   wait up to 10 minutes for backend reclamation. Uninstall `split-a` with
   `--wait --timeout 2m`. Its classes and Service must disappear and backing
   PVC remain. Shared snapshot class UID, driver and other servers must remain.
   Verify other servers' hashes. Create and restore a new snapshot of
   `split-b-iscsi` using the same shared class, verifying its hash within
   5 minutes for snapshot readiness and 5 minutes for restored mount readiness.
10. Complete Cleanup. Verify no owned resources remain except any explicitly
    retained diagnostics; compare host inventory to baseline.

## Expected

- Driver and server charts have independent resource ownership and values.
  Driver snapshot-class creation and Kasten annotation are independently
  configurable; both default to true. Delete and Retain render correctly.
  Missing snapshot APIs cause an actionable error with installation link and
  opt-out; disabling class creation succeeds without the API.
- Server capacity defaults to 50Gi and resources to no requests/limits. Explicit
  resource values render correctly, including requests-only and limits-only.
- One server alongside the driver, a server in another namespace, and two
  servers sharing that other namespace all provision through the same driver.
- Each server has distinct backing and endpoint identity. Destination prefixes
  default to release names, allow overrides, and reject invalid names.
  Cluster-wide class collisions fail without adopting or modifying resources.
- One driver-owned VolumeSnapshotClass snapshots and restores all six source
  claims on their respective backends with exact hash preservation. Server
  charts never create their own snapshot class.
- Independent upgrades preserve data and the other releases' resources.
  Removing an empty server leaves its source PVC, shared class and other
  releases intact; remaining backends can still snapshot and restore.
- Cleanup reclaims only run-owned resources and restores the host baseline.

## Evidence

Record chart/image versions and commit, renders/lint output, rejected values,
Helm release inventory, class manifests and ownership, source PVC/PV topology,
endpoint/VG/Service identities, snapshot/content handles, hashes, upgrade logs,
collision error and partial-resource inventory, and uninstall/cleanup results.
Report in `.test/reports/<date>-<instance>-independent-helm-charts.md`.

## Cleanup

Collect evidence after success or failure. Delete all run-owned consumers,
snapshots and restore PVCs with driver and servers still running; wait up to
10 minutes for physical reclamation. Inventory the failed collision release:
remove only its own resources, never the original `split-a` classes. If its
server started, follow guarded uninstall and verify host cleanup; if inspection
is blocked, report it rather than bypassing hooks. Uninstall remaining server
releases, verify owned kernel resources are gone, then explicitly delete their
retained source PVCs. Uninstall the driver last, removing its shared snapshot
class, and delete the run-owned namespaces. Do not remove an existing driver,
externally owned class, host resource or environment.

## Design notes

No live-data migration from the combined chart is required. Sharing a snapshot
class does not add cross-server clone/restore support. Retain policy is checked
by rendering here; the live cleanup path deliberately uses Delete. Kernel
restart behavior and nonempty/uninspectable uninstall are covered separately by
`pod-storage-server`. Published packages and repository indexing are covered by
`release-storage-server-image`.

## Observations

Passed on a fully recreated `eks-paris` cluster on 10 October 2026: one driver,
one server in its namespace and two servers in a separate namespace. Two
servers shared a worker. All six NFS/iSCSI snapshot restores used one shared
snapshot class and matched their source hashes. Independent upgrades/removal,
name collision rejection and host cleanup passed. Local report:
`.test/reports/2026-10-10-eks-paris-independent-helm-charts.md` (not committed).
Public repository publication and Kind runtime validation were not performed.
