---
id: kasten-backup-export-restore
status: manual
groups: [backup]
requires: [kubernetes, storage-server, nfs-client, iscsi-client, kasten, object-storage]
automation: none
---

# Kasten backs up lvmo volumes, exports them to S3, and restores from both the local and the exported restore point

## Purpose

lvmo is meant to be easy to protect with Kasten. A Kasten policy must snapshot lvmo volumes through the CSI snapshot class, export the backup to object storage, and restore the application both from the local snapshot (fast, same cluster) and from the exported copy (what survives the loss of the storage server). This uses Kasten's default filesystem export; block-mode export and changed block tracking are separate concerns.

## Preconditions

- StorageClasses `lvmo-nfs` and `lvmo-iscsi`; VolumeSnapshotClass `lvmo-snapshots` annotated `k10.kasten.io/is-snapshot-class: "true"`.
- Kasten installed in `kasten-io` and ready.
- An S3 bucket and credentials limited to it, in the environment's region.

## Steps

1. Create namespace `lvmo-kasten-app` with a 1Gi RWO PVC on `lvmo-iscsi` and a 1Gi RWX PVC on `lvmo-nfs`, mounted by one Deployment. Write 20 MiB of random data and a small text file to each volume, run `sync`, and record the SHA-256 of every file.
2. Create a Kasten location profile `lvmo-s3` for the bucket and wait for it to be validated.
3. Create a Kasten policy `lvmo-kasten-app-backup` for namespace `lvmo-kasten-app`: backup with snapshots, then export to `lvmo-s3` (filesystem export). Run it once with a RunAction **in `kasten-io`** (a RunAction must be in its policy's namespace) and wait for the backup and export actions to complete (timeout 30 minutes).
4. Record the restore points: one local, one exported.
5. Change the data: delete the text files and overwrite the random files.
6. **Local restore**: restore the namespace from the local restore point, replacing the existing application. Wait for the RestoreAction to complete and the Deployment to be Ready. Compute the checksums.
7. Retire the local restore point through Kasten, then delete namespace `lvmo-kasten-app`, so that only the exported copy can be used. Deleting the RestorePointContent directly leaves Kasten snapshot clones behind with policy Retain.
8. **Remote restore**: recreate namespace `lvmo-kasten-app`, create a RestorePoint in it bound to the exported RestorePointContent (`spec.restorePointContentRef`), and restore from that RestorePoint. Wait for the RestoreAction to complete and the Deployment to be Ready. Compute the checksums.

## Expected

- The location profile is valid; the backup and export actions complete without error.
- Step 6: every checksum equals step 1's, and the deleted text files are back.
- Step 8: every checksum equals step 1's.
- Restored PVCs use `lvmo-iscsi` and `lvmo-nfs` again, and are bound.

## Validation

Passed on `eks-paris` on 2026-10-04 with Kasten 9.0.6: backup 20 s, export 40 s, local and remote restores 2 minutes each, every checksum matching, no snapshot left on the storage server afterwards.

## Evidence

- Kasten profile, policy, RunAction, BackupAction, ExportAction and RestoreAction statuses.
- Restore point names and whether each is local or exported.
- Checksums at steps 1, 6 and 8.

## Cleanup

- Delete namespace `lvmo-kasten-app`, the policy, the restore points and the profile. Empty and delete the bucket and its credentials if the run created them. Check on the storage server that no lvmo volume or snapshot remains once Kasten has retired its snapshots.
