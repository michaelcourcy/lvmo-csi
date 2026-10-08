---
id: pod-storage-server
status: manual
groups: [basic]
requires: [kubernetes, nfs-client, iscsi-client, multi-node, distinct-initiators]
automation: none
---

# PVC-backed test storage server installed by Helm

## Purpose

Validate the [pod storage server](../../docs/pod-storage-server.md):
one source PVC supports a loop-backed thin pool and generated NFS/iSCSI classes,
without a separately provisioned storage host.

## Preconditions

- A named environment authorizes privileged server Pods and host kernel access
  on a dedicated Linux test node, with loop, device-mapper thin pools, kernel
  NFS and LIO support. No existing server occupies ports 2049, 3260 or 50061.
- An existing non-lvmo Filesystem StorageClass can supply a 5Gi RWO source PVC
  on that node. Record its provisioner, reclaim policy and binding mode. A local
  class may use WaitForFirstConsumer; placement must respect PV node affinity.
- A second node can mount both protocols. Snapshot CRDs/controller are installed.
- The development server image is available in the environment's permitted
  registry. No Helm release or generated class name collides with this test.
- Record baseline loop devices, VGs, NFS exports and iSCSI targets on the server
  node. Do not use the dedicated-VM cleanup-audit script on a shared host.

## Steps

1. Render/lint with the option disabled: no source PVC, server workload or
   generated StorageClasses must appear. With the option enabled, reject an
   empty source class, malformed size, or invalid destination class prefix.
2. Install release `lvmo-pod-test` into `lvmo-pod-test` with the values
   in the design, using the selected source class, `size=5Gi` and prefix
   `lvmo-test-sc`. Wait up to 10 minutes for source binding and API readiness.
   Verify one server replica, a loop device backed by the source PVC file, one
   owned VG/thin pool, and the two generated classes pointing at that backend.
3. Create namespace `lvmo-pod-consumers`. Create a 256Mi Filesystem PVC on each
   generated class (NFS RWX, iSCSI RWO). Mount each in a Pod on the second node.
   Within 5 minutes write a 16Mi test file, record its SHA-256 and sync it. Check
   the actual mount protocol and iSCSI portal, not just Pod readiness.
4. Create a dedicated VolumeSnapshotClass for `lvmo.csi.io` and a snapshot of
   each PVC. Wait up to 5 minutes for readyToUse. Restore each into a new 256Mi
   PVC of the same class and verify both hashes from Pods on the second node.
5. Stop consumer Pods cleanly, keep their PVCs, and delete the server Pod with
   normal graceful termination. Allow its replacement 10 minutes to become
   ready. Recreate consumers and verify both original and restored hashes.
   Confirm that neither the backing image nor API state was initialized again,
   and that there is only one loop attachment for the backing file.
6. Perform an unchanged Helm upgrade. Verify class endpoints and volume handles
   remain stable and data is readable. Record rejection of unsupported changes
   to backing-store identity; do not apply destructive migration workarounds.
7. While consumer volumes and snapshots exist, attempt an explicit disable,
   and an upgrade omitting the switch with `--reset-values`.
   Each must fail before removing server, CSI driver or generated classes.
   Verify consumer data remains readable after each refusal. An upgrade omitting
   the switch with `--reuse-values` must retain the enabled server and data.
8. Verify the guard also refuses removal when the server cannot be inspected;
   restore server availability afterward. A failed inspection must not be
   interpreted as an empty backend. Use a bounded, reversible interruption of
   the guard's access rather than force-deleting a server with mounted storage.
9. Remove all consumers and snapshots and wait for physical reclamation. Disable
   the option and verify successful server/class removal while the source PVC
   is retained. Re-enable against the retained source and verify readiness with
   the existing pool and state. Create a fresh 256Mi iSCSI consumer and write a
   hash-checked file. Attempt uninstall: it must fail while leaving the data
   readable. Helm can mark the release `uninstalling` after a failed pre-delete
   hook; do not assume an upgrade can follow that failure. Delete the consumer,
   wait for backend reclamation, then retry uninstall successfully and verify
   source-PVC retention again. Finish Cleanup and record all results.

## Expected

- Opt-in installation creates one server in the CSI namespace and exactly two
  non-default destination classes; disabled installation is unchanged.
- Both protocols provision and mount from another node; snapshot restores and
  a graceful server replacement preserve exact file hashes.
- Restart reuses existing storage and leaves no duplicate owned attachments.
- The source class is independent of lvmo; its local placement constraints are
  honored. Reported thin-pool capacity is smaller than the backing PVC capacity.
- Disable/reset-to-disabled and uninstall are refused while the backend is
  nonempty or cannot be inspected. Refusal leaves serving resources intact.
  Enabled upgrades, including omission with value reuse, preserve data.
- After reclamation, disable and uninstall succeed and stop the server. The
  retained source can be reused on re-enable without reinitialization.
- Uninstall retains the source PVC for explicit deletion. Cleanup removes only
  test-owned kernel resources; baseline resources are unchanged.

## Evidence

- Versions, commit, rendered chart, source and generated class manifests, source
  PV affinity and node placement, readiness and action timings.
- PVC/snapshot handles, mount/portal evidence, hashes before and after restore
  and restart, and owned loop/VG inventory before and after replacement.
- Per-step results, server logs, cleanup inventory and source-PVC retention proof.

## Cleanup

- After success or failure, collect evidence and delete consumer Pods, snapshots
  and consumer PVCs while the server is running. Wait up to 10 minutes for owned
  volumes, mounts and targets to be reclaimed.
- Uninstall the test release and verify graceful server shutdown removes owned
  host attachments. Verify the source PVC still exists, then explicitly delete
  it and wait for its source provisioner's reclamation. Do not remove a backing
  file while a loop device still references it.
- Delete the dedicated snapshot class and test namespaces after their resources
  are gone. Compare host inventory to baseline; record leftovers as failures.
- Retain diagnostic evidence and report any blocked cleanup instead of touching
  unrelated host storage.

## Observations

Validated on EKS on 8 October 2026: cross-node NFS/iSCSI writes, both snapshot
restores, server replacement with all four file hashes preserved, unchanged and
reused-value upgrades, rejection of nonempty explicit/reset disable, rejection
of unreachable inspection, empty disable/re-enable preserving VG UUID, and
nonempty/empty uninstall with source-PVC retention. Local report:
`.test/reports/2026-10-08-eks-paris-pod-storage-server.md` (not committed).
The source class was EBS gp3; this does not validate every possible source CSI
filesystem or abrupt server/node loss.
