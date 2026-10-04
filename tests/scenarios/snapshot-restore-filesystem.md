---
id: snapshot-restore-filesystem
status: automated
groups: [basic, backup]
requires: [kubernetes, storage-server, nfs-client, iscsi-client]
automation: scripts/run-scenarios.sh snapshot-restore-filesystem
---

# A filesystem snapshot restores the same data, in the same and in another namespace

## Purpose

Ordinary snapshot-based backup and restore must work for `Filesystem` PVCs on both protocols, independently of block export and changed block tracking.

## Preconditions

- StorageClasses `lvmo-nfs` and `lvmo-iscsi`, and VolumeSnapshotClass `lvmo-snapshots`, as installed by `scripts/install-storageclasses.sh`.
- The snapshot controller and snapshot CRDs are installed.

## Steps

Run once per StorageClass (`lvmo-nfs`, then `lvmo-iscsi`). The script uses the manifests of the pinned upstream suite [michaelcourcy/test-csi-snapshot](https://github.com/michaelcourcy/test-csi-snapshot).

1. Create namespaces `lvmo-snapshot-test` and `lvmo-snapshot-restore`.
2. Create a PVC on the StorageClass and a pod that writes `test data` to `/data/test-file`. Wait up to 180 s for the pod to be Ready, then run `sync` in the pod.
3. Create a VolumeSnapshot of the PVC. Wait up to 180 s for `readyToUse=true`.
4. Create a PVC from the snapshot in the same namespace and a pod mounting it. Wait up to 180 s for the pod to be Ready.
5. Read `/data/test-file` from that pod.
6. Create a pre-provisioned VolumeSnapshotContent `restored-snapcontent` referencing the snapshot handle from step 3, with `deletionPolicy: Retain`, and a VolumeSnapshot bound to it in `lvmo-snapshot-restore`.
7. Create a PVC from that snapshot in `lvmo-snapshot-restore` and a pod mounting it. Wait up to 180 s for the pod to be Ready.
8. Read `/data/test-file` from that pod.

Automated: `scripts/run-scenarios.sh snapshot-restore-filesystem` runs `scripts/test-snapshots.sh` for both StorageClasses.

## Expected

- The snapshot becomes ready within 180 s.
- Both restored pods become Ready and read exactly `test data`.

## Evidence

- Script output for each StorageClass, ending with `Upstream snapshot suite passed for <class>`.

## Cleanup

- Delete both namespaces and `restored-snapcontent` (the script does this on exit).
