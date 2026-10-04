---
id: iscsi-takeover-after-node-failure
status: manual
groups: [resilience]
requires: [kubernetes, storage-server, iscsi-client, multi-node, distinct-initiators, node-power-control]
automation: none
---

# An iSCSI volume moves to another node after its node is confirmed dead, and the dead node is fenced

## Purpose

An RWO iSCSI volume is owned by the node that staged it, so that two nodes never mount the same ext4 or xfs filesystem. Today that ownership is only released by `NodeUnstageVolume`. If the node dies, the volume stays owned and a rescheduled pod cannot start elsewhere. Once a node is confirmed dead, the volume must move, and the old node must no longer be able to write to it, even if it comes back.

## Preconditions

- StorageClass `lvmo-iscsi`.
- Every worker has a distinct iSCSI initiator name (`/etc/iscsi/initiatorname.iscsi`).
- No MachineHealthCheck or autoscaler will replace the node during the test, or it is paused.

## Steps

1. Create namespace `lvmo-takeover` and a 1Gi RWO `Filesystem` PVC `data` on `lvmo-iscsi`.
2. Create a single-replica Deployment whose pod mounts `data` and appends a timestamp line to `/data/log` every second. Wait for Ready (180 s). Record the node, call it node A.
3. After 30 s, record the last line of `/data/log`.
4. Power node A off (not a graceful drain). Wait until the Node object is `NotReady`.
5. Apply the taint `node.kubernetes.io/out-of-service=nodeshutdown:NoExecute` to node A.
6. Wait up to 5 minutes for the replacement pod to be Ready on another node, node B.
7. Read `/data/log` from the new pod and check that it still contains the line recorded in step 3, followed by new lines from node B.
8. Power node A back on **without** removing the taint. On node A, try to log in to the volume's iSCSI target with `iscsiadm`, and if it succeeds try to write to the device.
9. Remove the taint from node A.

## Expected

- The replacement pod becomes Ready on node B within 5 minutes of the taint (step 6), with no manual action on the storage server.
- The log written before the failure is intact, and the filesystem mounts without errors on node B.
- After the takeover, the storage server lists only node B as allowed to access the volume.
- In step 8, node A cannot read from or write to the volume: the target refuses its login or its I/O.
- Without the taint (between steps 4 and 5), the volume is **not** handed to node B.

## Evidence

- Pod placement before and after, with timestamps of the power-off, the taint and the pod becoming Ready.
- The volume's ownership and the target's ACL on the storage server before and after.
- The output of node A's login and write attempts in step 8.
- `dmesg` from node B showing the filesystem journal replay.

## Cleanup

- Remove the taint if still present. Delete namespace `lvmo-takeover`.
- On node A, log out of any remaining session for the volume.
- Check on the storage server that no LV, target or ownership entry remains.

## Validation

Passed on `eks-paris` (EKS 1.35, 3 Ubuntu workers) on 2026-10-03 with image `dev-iscsi-takeover`: the volume stayed blocked with a Multi-Attach error for 7 minutes without the taint, moved to node B 7 seconds after the taint, kept its data through an ext4 journal recovery, and node A's login was refused with an authorization failure after it came back. The failed node also hosted the lvmo controller, which Kubernetes moved during the takeover.

## Design notes

- The node is declared dead by the `out-of-service` taint, not by a timeout alone: an unreachable node may still be writing.
- The fencing is enforced at the iSCSI target (per-initiator ACL, session closed), not only in the API's ownership table. Preferred approach: `attachRequired: true`, a `csi-attacher` sidecar, and `ControllerPublishVolume` / `ControllerUnpublishVolume` adding and removing the node's initiator from the target ACL, so that Kubernetes' standard non-graceful shutdown handling drives the takeover. NFS volumes accept the attach call without doing anything.
- A heartbeat-based takeover may be added later as an alternative trigger, but always with the same target-side fencing.
