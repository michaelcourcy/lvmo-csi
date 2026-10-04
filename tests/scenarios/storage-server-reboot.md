---
id: storage-server-reboot
status: manual
groups: [resilience]
requires: [kubernetes, storage-server, storage-server-reboot, nfs-client, iscsi-client]
automation: none
---

# Volumes, exports, targets and snapshots survive a reboot of the storage server

## Purpose

All durable state lives on the storage server: LVM metadata on the physical volumes, and the API's state in `/var/lib/lvmo/state.json`. After a reboot the API must bring every ready volume back (activate it, remount and re-export NFS volumes, recreate iSCSI targets) without anyone recreating the VG or the pool. A loop-backed lab must re-attach its disk before the API starts (`lvmo-loop.service`, see the Create section of [lima.md](../environments/lima.md)).

## Preconditions

- StorageClasses `lvmo-nfs` and `lvmo-iscsi`, VolumeSnapshotClass `lvmo-snapshots`.
- `lvmo-api.service` is enabled. If the VG sits on a loop device, `lvmo-loop.service` is enabled and `lvmo-api.service` requires it.

## Steps

1. Create namespace `lvmo-reboot`. Create a 1Gi PVC `nfs-data` on `lvmo-nfs` and a 1Gi PVC `iscsi-data` on `lvmo-iscsi`, both `Filesystem`.
2. Create a pod mounting each PVC. Write 50 MiB of random data to `/data/blob` in each, run `sync`, and record the SHA-256 of each file.
3. Create a VolumeSnapshot of each PVC and wait for `readyToUse=true` (180 s).
4. On the storage server, record `lvs`, `exportfs -v`, `targetcli ls /iscsi` and the volume and snapshot ids in `state.json`.
5. Reboot the storage server. Wait until it answers SSH, then until `systemctl is-active lvmo-api` is `active` (timeout 5 minutes).
6. Repeat the records of step 4.
7. Delete both pods. Create new pods mounting the same PVCs and wait for Ready (180 s). Compute the SHA-256 of `/data/blob`.
8. Create a PVC from each snapshot, mount each in a pod, and compute the SHA-256 of `/data/blob`.

## Expected

- `lvmo-api` becomes active without manual intervention and without any `pvcreate`, `vgcreate` or `lvcreate`.
- After the reboot, `lvs` shows the same volume and snapshot LVs as before, all active.
- `exportfs -v` lists the NFS volume again and `targetcli ls /iscsi` lists the iSCSI volume's target again.
- The checksums from steps 7 and 8 equal those from step 2.

## Evidence

- The records from steps 4 and 6, side by side.
- `journalctl -u lvmo-loop -u lvmo-api -b` from the storage server (boot after the reboot).
- The three sets of checksums.

## Cleanup

- Delete namespace `lvmo-reboot`; wait until its PVs and VolumeSnapshotContents are gone.
- Check on the storage server that no LV, export or target for these volumes remains.

## Observations

- Whether the pods running during the reboot resumed I/O by themselves, and how long it took. NFS hard mounts usually wait and resume; iSCSI sessions fail I/O if the outage exceeds the initiator's `replacement_timeout` (120 s by default). This is not a pass criterion.
