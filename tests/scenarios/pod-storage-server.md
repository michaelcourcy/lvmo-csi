---
id: pod-storage-server
status: manual
groups: [basic]
requires: [kubernetes, nfs-client, iscsi-client, multi-node, distinct-initiators]
automation: none
---

# PVC-backed test storage server installed by Helm

> The two-chart revision passed on EKS on 10 October 2026.
> Earlier observations below apply to the combined chart.

## Purpose

Validate the [pod storage server](../../docs/pod-storage-server.md):
one source PVC supports a loop-backed thin pool and generated NFS/iSCSI classes,
without a separately provisioned storage host, using the Pod network behind a
stable ClusterIP Service and requiring no node/IP configuration.

## Preconditions

- A named environment authorizes privileged server Pods and host kernel access
  on eligible Linux test nodes, with loop, device-mapper thin pools, kernel
  NFS and LIO support and an IPv4 Pod network. All clients can reach the Service network. Record the
  baseline host listeners; Pod networking must not replace them. Use trusted
  test clients: NFS exports default to `*`.
- An existing non-lvmo Filesystem StorageClass can supply a 5Gi RWO source PVC
  on an eligible node. Record its provisioner, reclaim policy and binding mode. A local
  class may use WaitForFirstConsumer; placement must respect PV node affinity.
- The instance permits temporarily cordoning the server node without eviction.
- A second node can mount both protocols. Snapshot CRDs/controller are installed.
- The development server image is available in the environment's permitted
  registry. No Helm release or generated class name collides with this test.
- Record baseline loop devices, VGs, NFS exports and iSCSI targets on the server
  nodes. Do not use the dedicated-VM cleanup-audit script on a shared host.

## Steps

1. Run `bash scripts/test-pod-storage-chart.sh`. Lint/render both charts; successful offline driver rendering must
   advertise `--api-versions snapshot.storage.k8s.io/v1/VolumeSnapshotClass`,
   while offline lint can use `--set snapshotClass.enabled=false`. The driver must create no server PVC,
   server workload or destination StorageClasses. Reject legacy
   `create-storage-server` values with an actionable error. The server chart
   must create no driver resources or VolumeSnapshotClass. Reject an empty
   source class, malformed size, invalid destination prefix and a source class
   provisioned by lvmo. Render without node/IP/client-selector values.
2. Install driver release `lvmo-driver-test` in namespace `lvmo-pod-test` from
   `charts/lvmo-csi`, with `snapshotClass.enabled=true`,
   `snapshotClass.name=lvmo-pod-test-snapshots` and `snapshotClass.kasten=true`.
   Use the environment-authorized driver image and allow 5 minutes for readiness.
   Install server release `lvmo-pod-test` in that namespace from
   `charts/lvmo-csi-storage-server`, with the selected `source-storage-class`,
   `size=5Gi`, `dest-storage-class-prefix=lvmo-test-sc` and authorized server image.
   Wait up to 10 minutes for source binding and API readiness.
   Verify one server replica, a loop device backed by the source PVC file, one
   owned VG/thin pool, and the two generated classes pointing at that backend.
   Verify the driver-owned VolumeSnapshotClass `lvmo-pod-test-snapshots` exists with driver
   `lvmo.csi.io`, `deletionPolicy: Delete` and annotation
   `k10.kasten.io/is-snapshot-class: "true"`. Verify `lvmo-test-sc-iscsi`
   carries `k10.kasten.io/sc-supports-block-mode-exports: "true"` and
   `lvmo-test-sc-nfs` carries no block-mode export annotation.
   Verify `hostNetwork: false`, no hostname selector, a Pod IP distinct from the
   host IP, and a ClusterIP Service exposing TCP 50051, 2049 and 3260. Record its
   IP and EndpointSlice Pod address. Both class endpoints must use
   `lvmo-pod-test-storage.lvmo-pod-test.svc:50051`. Record the server node and
   select a different node for the consumers in subsequent steps.
3. Create namespace `lvmo-pod-consumers`. Create a 256Mi Filesystem PVC on each
   generated class (NFS RWX, iSCSI RWO). Mount each in a Pod on the second node.
   Within 5 minutes write a 16Mi test file, record its SHA-256 and sync it. Check
   the actual mount protocol and iSCSI portal, not just Pod readiness.
   Use `findmnt -t nfs,nfs4` and `iscsiadm -m session -P 3` on the client node:
   the NFS source and active iSCSI portal must use the Service IP, not the Pod or
   node IP. Inspect `targetcli ls` inside the server: its listener must bind the
   Pod IP with no wildcard portal. The driver must not depend on SendTargets
   discovery to create its Service-addressed node records.
4. Using the shared VolumeSnapshotClass `lvmo-pod-test-snapshots`, create a
   snapshot of each PVC. Wait up to 5 minutes for readyToUse. Restore each into a new 256Mi
   PVC of the same class and verify both hashes from Pods on the second node.
5. Stop consumer Pods cleanly, keep their PVCs, and delete the server Pod with
   normal graceful termination. Allow its replacement 10 minutes to become
   ready. Record old/new Pod IPs and verify the Service IP is unchanged and its
   EndpointSlice selects only the new Pod IP. If the CNI reuses the Pod IP,
   repeat graceful replacement up to three times; a changed Pod IP is required
   to validate this step. Recreate consumers and verify both original and
   restored hashes. Recheck NFS source and iSCSI sessions: both must still use
   the Service IP, with no session using the old Pod IP.
   Confirm that neither the backing image nor API state was initialized again,
   and that there is only one loop attachment for the backing file.
   Then keep both original consumers mounted, gracefully replace the server
   once more, and allow up to 5 minutes after API readiness for reads/writes to
   recover. Use bounded commands (`timeout 300`) to verify the saved hashes and
   write/read a fresh 1Mi file through both mounts. Record reconnection timing;
   do not restart the consumers to make this check pass.
6. Upgrade the driver with its saved values, then independently upgrade the
   server with its saved values (5 minutes per upgrade). Record server Pod UID,
   Service IP, PVC UID and VG UUID before/after the driver upgrade; they must
   remain unchanged. Record driver Pod UIDs and shared snapshot-class UID
   before/after the server upgrade; they must remain unchanged. Verify endpoints,
   handles and hashes remain stable. Attempt server upgrades changing each of
   source class (to another name), size (to `6Gi`) and prefix (to `changed-test`);
   each must be rejected without changing backend identity. An unchanged server
   upgrade with `--reuse-values` must preserve data too.
7. While consumers and snapshots exist, run
   `helm uninstall lvmo-pod-test -n lvmo-pod-test --wait --timeout 2m`.
   It must fail without deleting server resources. Verify the inspection Job
   runs on the server's node through Pod affinity and data remains readable.
   The driver and shared snapshot class must remain unchanged. Helm may mark
   the server release `uninstalling`; do not require an upgrade after refusal.
8. Verify uninstall also fails when inspection cannot run: cordon the recorded
   server node without evicting its running Pods, retry the same uninstall with
   its 2-minute timeout, and record the pending guard Job and scheduling reason.
   Confirm the existing server and consumers remain running. Restore the node's
   original schedulability immediately, including after failure; only perform
   this step where the environment authorizes temporary cordoning. A failed
   inspection must not be interpreted as an empty backend.
9. Delete all consumer Pods, PVCs and snapshots. Wait up to 10 minutes for
   physical reclamation, then retry the server uninstall successfully. Verify
   its workload, Service and two StorageClasses are gone, its source PVC remains,
   and the driver and shared snapshot class still exist. Finish Cleanup.

## Expected

- Two independent releases install one driver and one server in the same
  namespace. The server owns exactly two non-default StorageClasses (iSCSI
  alone annotated for Kasten block export), its backing PVC and Service. The
  driver owns the single shared snapshot class with Kasten annotation.
- No node name, server address or NFS client selector is required. Invalid
  server settings and legacy combined-chart values are rejected.
- The server uses its Pod network and source-PV topology for placement. NFS
  mounts and iSCSI client sessions use the stable Service IP; iSCSI listeners
  bind only the current Pod IP. Pod replacement changes the endpoint without
  changing the Service IP or requiring client configuration changes.
- Both protocols provision and mount from another node; snapshot restores and
  a graceful server replacement preserve exact file hashes. Existing mounted
  consumers reconnect after replacement and accept new writes within 5 minutes
  of API readiness.
- Restart reuses existing storage and leaves no duplicate owned attachments.
- The source class is independent of lvmo; its local placement constraints are
  honored. Reported thin-pool capacity is smaller than the backing PVC capacity.
- Server uninstall is refused while the backend is nonempty or inspection
  fails. Refusal leaves serving resources intact. Independent unchanged
  upgrades preserve data and do not roll the other component; unsupported
  backing identity changes are rejected.
- After reclamation, server uninstall removes its workload, Service and classes
  without removing the driver or shared snapshot class. No disable/re-enable
  or migration of a retained backend to a recreated Service is required.
- Uninstall retains the source PVC for explicit deletion. Cleanup removes only
  test-owned kernel resources; baseline resources are unchanged.

## Evidence

- Versions, commit, rendered chart, source and generated class manifests, source
  PV affinity and node placement, readiness and action timings.
- Service IP, old/new Pod IPs, EndpointSlices, guard placement, client session
  portals and server portal bindings, plus mounted-client reconnect timings.
- PVC/snapshot handles, mount/portal evidence, hashes before and after restore
  and restart, and owned loop/VG inventory before and after replacement.
- Per-step results, server logs, cleanup inventory and source-PVC retention proof.

## Cleanup

- Restore the server node's original schedulability if step 8 was interrupted.
- After success or failure, collect evidence and delete consumer Pods, snapshots
  and consumer PVCs while the server is running. Wait up to 10 minutes for owned
  volumes, mounts and targets to be reclaimed.
- Uninstall the server test release and verify graceful server shutdown removes owned
  host attachments. Verify the source PVC still exists, then explicitly delete
  it and wait for its source provisioner's reclamation. Do not remove a backing
  file while a loop device still references it.
- Uninstall the run-owned driver last; this removes its shared snapshot class.
  Delete the test namespaces after their resources are gone. Compare host inventory to baseline; record leftovers as failures.
- Retain diagnostic evidence and report any blocked cleanup instead of touching
  unrelated host storage.

## Observations

The previous host-network implementation was validated on EKS on 8 October 2026: cross-node NFS/iSCSI writes, both snapshot
restores, server replacement with all four file hashes preserved, unchanged and
reused-value upgrades, rejection of nonempty explicit/reset disable, rejection
of unreachable inspection, empty disable/re-enable preserving VG UUID, and
nonempty/empty uninstall with source-PVC retention. Local report:
`.test/reports/2026-10-08-eks-paris-pod-storage-server.md` (not committed).
The source class was EBS gp3; this does not validate every possible source CSI
filesystem or abrupt server/node loss.

The Pod-network/Service revision passed on `eks-paris` on 9 October 2026 after
fixing D-Bus startup ordering before targetcli. Both snapshot restores matched
16MiB source hashes. Two graceful replacements changed the Pod IP while keeping
the Service IP; existing mounted clients recovered in 3 seconds (iSCSI) and
92 seconds (NFS) after API readiness, with new writes verified. All upgrade and
removal checks passed, including retained-pool reuse. Local report:
`.test/reports/2026-10-09-eks-paris-pod-storage-server.md` (not committed).
NFS client recovery tracking was unavailable in the server; this run validates
file I/O after graceful replacement, not lock/delegation reclaim.

The independent-chart revision passed on a fully recreated `eks-paris` cluster
on 10 October 2026 with EBS gp3 backing. All nine steps passed, including both
restore hashes, changed Pod IPs, independent upgrades, both uninstall refusal
paths and explicit deletion of the retained source PVC. Mounted clients
recovered in 1.4 seconds (iSCSI) and 89 seconds (NFS), with new writes verified.
Replacement containers briefly retried while the old backing-store lock was
held, then became ready within the timeout. Host loop inventories matched the
baseline after cleanup. Local report:
`.test/reports/2026-10-10-eks-paris-pod-storage-server.md` (not committed).
