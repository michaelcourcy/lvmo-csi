---
id: kasten-block-mode-export
status: manual
groups: [backup]
requires: [kubernetes, storage-server, nfs-client, iscsi-client, kasten, object-storage]
automation: none
---

# Kasten iSCSI block-mode export/restore and NFS restore limitation

## Purpose

Validate Kasten's optional block-mode export with the approximately 50 GB PostgreSQL workload used in [kasten-export-performance](kasten-export-performance.md). For an iSCSI Filesystem PVC, restore the initial export and the export after deleting 20% of the accounts, and compare exact row counts. For an NFS Filesystem PVC, demonstrate export through an alternate iSCSI class and document why direct restoration to NFS is unsupported in the tested integration.

The NFS case is a limitation check, not a usable NFS backup/restore workflow. Customers needing an NFS restore should use the ordinary filesystem-export path. Successful block export does not establish KEP-3314 consumption or CBT compatibility.

## Preconditions

- An explicitly selected environment instance with all required capabilities and permission to install or upgrade the components involved. Use [eks](../environments/eks.md) as the construction guide for the intended run; the type file is not itself an instance or permission to create infrastructure. Follow AGENTS.md for instance creation and lifecycle decisions after scenario approval.
- Record the lvmo commit, driver/API versions, Kubernetes and Kasten versions. Kasten must support the StorageClass export annotation documented below (documentation reviewed for 9.0.7).
- Workers have working NFS clients and distinct, working iSCSI initiators. Provide capacity for PostgreSQL requests of 2 CPU and 8 GiB plus Kasten data movers and one restore at a time. The default EKS t3.medium workers need resizing for this workload; use the performance scenario's m6i.2xlarge workers and m6i.xlarge storage server as a sizing reference.
- A dedicated real data disk and thin pool in VG `lvmo-block-export`, with at least 300 GiB available initially. Do not use the EKS guide's small loop disks for this workload. Run protocols and restores sequentially. Monitor thin-pool data and metadata usage throughout; stop new work at 80% and resolve capacity within the instance's permissions before continuing.
- Two dedicated StorageClasses, `lvmo-block-iscsi` and `lvmo-block-nfs`, with the lvmo provisioner, the same API endpoint, VG `lvmo-block-export`, `filesystem: ext4`, `reclaimPolicy: Delete`, and protocols `iscsi` and `nfs` respectively. Use RWO Filesystem claims for both application workloads.
- A dedicated lvmo VolumeSnapshotClass `lvmo-block-snapshots`, annotated `k10.kasten.io/is-snapshot-class: "true"`. Ensure Kasten selects this class without changing unrelated snapshot-class defaults.
- Kasten in `kasten-io`, its own storage outside the test VG, and a validated object-storage location profile `kasten-block-s3`. Credentials and the destination must be limited to the test's authorized scope.
- Complete the [Kasten Primer block mount check](https://docs.kasten.io/latest/usage/protect/#enabling-block-mode-export) for the iSCSI export class and save its result. An NFS class is not a raw-block mount provider; its export path must use the alternate iSCSI class.
- Use a 30-minute deadline for workload/PVC readiness, 2 hours for initialization or SQL maintenance, and 4 hours for each backup/export or restore. Record timeout failures rather than silently extending deadlines.

## Steps

1. Record the environment preflight, component versions, StorageClasses, snapshot class, location profile validation, and baseline storage inventory. Start collecting test-related PVC/PV, VolumeSnapshot/VolumeSnapshotContent and data-mover Pod manifests during operations, before temporary resources disappear. Exclude secret values from evidence.
2. Configure the dedicated classes:

   ```sh
   kubectl annotate storageclass lvmo-block-iscsi \
     k10.kasten.io/sc-supports-block-mode-exports=true
   kubectl annotate storageclass lvmo-block-nfs \
     k10.kasten.io/sc-supports-block-mode-exports=true \
     k10.kasten.io/export-storage-class=lvmo-block-iscsi
   ```

   The NFS annotation opts its application PVCs into this integration path; it must not cause a raw Block NFS claim. Read back both classes. Do not configure `exportData.overrides` or `exportData.exporterStorageClassName` on policies or actions: these would override the StorageClass annotation being tested.
3. **iSCSI source:** create namespace `pg-block-iscsi`, an 80Gi RWO PVC `data` with explicit `volumeMode: Filesystem` and StorageClass `lvmo-block-iscsi`, and a single-replica Deployment `postgres` using `postgres:16`. Mount the PVC at `/var/lib/postgresql/data`, set `PGDATA=/var/lib/postgresql/data/pgdata`, and use database/user `pgbench` with credentials held in a Kubernetes Secret. Set `shared_buffers=2GB`, `max_wal_size=8GB`, and requests of 2 CPU and 8 GiB. Configure `Recreate` deployment strategy. Wait for PostgreSQL readiness.
4. Annotate the source PVC before any backup:

   ```sh
   kubectl -n pg-block-iscsi annotate pvc data \
     k10.kasten.io/pvc-export-volume-in-block-mode=force
   ```

   In the PostgreSQL container, run `pgbench -U pgbench -d pgbench -i -s 3400 -I dtgvp`, then execute `CHECKPOINT;`. Record duration and results of `SELECT pg_database_size('pgbench');` and `SELECT count(*) FROM pgbench_accounts;` using `psql -U pgbench -d pgbench -v ON_ERROR_STOP=1`. The initial count must be 340,000,000. Do not run a transaction benchmark or other application writes during backups.
5. Create on-demand Kasten policy `pg-block-iscsi-export` selecting only `pg-block-iscsi`, taking a CSI snapshot and exporting volume data to `kasten-block-s3` (not just snapshot references). Retain both generations through verification. Run the policy and wait for its RunAction, BackupAction and ExportAction to complete. Label this generation `full` in the report and record its exported restore-point identifier.
6. During export, capture the temporary clone's `volumeMode: Block`, `storageClassName: lvmo-block-iscsi`, its snapshot data source, the associated PV's iSCSI protocol, and the data-mover Pod's `volumeDevices`. Capture `snapshot.storage.kubernetes.io/allow-volume-mode-change: "true"` on the VolumeSnapshotContent actually used by the Block clone, which may be a Kasten-created copy rather than the original content. Verify Kasten or its configured integration supplies this permission; if absent and conversion fails, record the failure before making any integration change and rerun from the start after the fix. Save action details/logs establishing block export, not merely successful action status.
7. **Restore the full export:** explicitly select the exported, object-storage restore point, not its local snapshot counterpart. Restore into namespace `pg-block-iscsi-full`, using the source StorageClass and Filesystem application volume mode. Capture the RestoreAction selection and data-mover download evidence proving that volume data came from object storage. Wait for PostgreSQL readiness and run `SELECT count(*) FROM pgbench_accounts;`; require 340,000,000. Verify the restored PVC uses `lvmo-block-iscsi` and the application mounts a filesystem. Delete this restored workload/namespace and wait for its storage to be reclaimed before proceeding.
8. **Delete 20% on the source:** in `pg-block-iscsi`, execute these commands separately with `ON_ERROR_STOP`:

   ```sql
   DELETE FROM pgbench_accounts WHERE aid <= 68000000;
   VACUUM pgbench_accounts;
   CHECKPOINT;
   SELECT pg_database_size('pgbench');
   SELECT count(*) FROM pgbench_accounts;
   SELECT count(*) FROM pgbench_accounts WHERE aid <= 68000000;
   ```

   Record `DELETE 68000000`, a remaining count of 272,000,000, and zero accounts in the deleted range. Record duration and database size. Keep the first export retained and use the same PVC, policy and location profile.
9. **Export and restore the changed database:** run the same policy again, record generation `after-delete`, and repeat step 6's block-path evidence. Restore this second exported restore point into `pg-block-iscsi-after-delete` following step 7. Require 272,000,000 accounts and zero rows with `aid <= 68000000`. Capture object-storage download evidence and the restored Filesystem PVC. Remove the restore namespace, then retire the iSCSI case's backups and remove its source resources using Cleanup before beginning the NFS case.
10. **NFS export and restore limitation:** repeat steps 3–6 with source namespace `pg-block-nfs`, application StorageClass `lvmo-block-nfs`, and policy `pg-block-nfs-export`. Apply the same `force` annotation to its `data` PVC and initialize a fresh database at scale 3400. Require export clones on **`lvmo-block-iscsi` in Block mode**, linked to snapshots of the **NFS** source. Verify the source remains a Filesystem PVC on `lvmo-block-nfs` and PostgreSQL continues using NFS. Explicitly select the full exported restore point and attempt restoration into `pg-block-nfs-full` on the original NFS class. Capture the restore's temporary PVC/PV protocol, volume mode, download Pod and attachment events. In Kasten 9.0.7, the download requests raw Block access to the NFS target and lvmo rejects it with `NFS requires filesystem access`. Once this cause is established, cancel the RestoreAction through Kasten and wait for cancellation and temporary-resource cleanup; do not wait out the four-hour deadline for a known unsupported operation. An unrelated failure does not establish this limitation. If a newer integration restores successfully, record its path, final NFS mount and row count, and report the changed behavior for review. Do not substitute an iSCSI application volume or manually rebind the backend LV. Do not run an NFS after-deletion generation: it cannot establish NFS recovery while the full restore is unsupported.
11. Write the report with the two iSCSI export/restore results and the separate NFS export result and restore limitation. Include row counts, block-path evidence, timings and cleanup outcomes. If the instance provides `test-storage-server`, execute `cleanup-audit` last after all scenario cleanup.

## Expected

- Both source applications use Filesystem PVCs throughout: iSCSI for the first case, NFS for the second.
- The two iSCSI backup/export runs and the NFS full backup/export complete and produce exported restore points containing volume data. All three exports demonstrably use iSCSI Block clones and block data movers, with no filesystem-export fallback.
- NFS export clones use the alternate class selected by `k10.kasten.io/export-storage-class`, with no policy/action override masking that annotation.
- Both iSCSI restores read exported data from object storage and complete successfully. Restored applications use Filesystem PVCs on `lvmo-block-iscsi` and PostgreSQL becomes ready within the deadline. Raw Block access is required by the data mover, not by the final application PVC.
- The initial iSCSI restore contains exactly 340,000,000 rows in `pgbench_accounts`. The after-delete restore contains exactly 272,000,000 rows and none with `aid <= 68000000`.
- The NFS limitation check identifies rejection of raw Block access on the NFS restore target and cancels the restore cleanly. It must be reported as an unsupported NFS restore workflow, never as successful NFS recovery. No restored NFS row count or after-deletion result is claimed.
- Cleanup removes the run's workloads, temporary clones, snapshots, exports and iSCSI targets/sessions without deleting unrelated resources.

## Evidence

- Report at `.test/reports/<YYYY-MM-DD>-<instance>-kasten-block-mode-export.md`, including instance capabilities, lvmo commit/version, component versions and per-step passed/failed/not-run status.
- Redacted manifests of classes, policies, annotated source PVCs, temporary clone PVCs/PVs, data-mover Pods, snapshots/contents and restored PVCs; correlate their UIDs/handles to each action and generation.
- RunAction, BackupAction, ExportAction and RestoreAction identifiers, statuses, errors, timestamps and restore-point identifiers. Preserve proof of object-storage restore selection/download and raw-block export access while temporary resources exist.
- Source SQL outputs and both iSCSI restored SQL outputs, initialization/deletion timings, database sizes, and thin-pool data/metadata usage before, during and after each case.
- Code or configuration changes made after failures, rerun results, cleanup inventory compared with baseline, and cleanup-audit report when applicable.

## Cleanup

- Run after success or failure. Collect evidence first; stop test policies and wait for or cancel in-flight test actions through Kasten before deleting their dependencies.
- Delete restore namespaces. Retire only this scenario's restore points through Kasten: delete their RestorePoints and corresponding RestorePointContents, then wait for RetireActions to complete. Do not directly remove repository objects from a shared bucket.
- Delete source namespaces and dedicated policies. Wait for test PVCs/PVs, temporary export/restore clones, VolumeSnapshots and VolumeSnapshotContents to disappear; check the storage server for remaining test LVs, exports, targets and sessions against the baseline. Allow 30 minutes for reclamation; record leftovers as cleanup failures.
- Delete the dedicated StorageClasses and snapshot class after dependent resources are gone. Remove the location profile, credentials and bucket only if created exclusively for this run and after retirement completes.
- Restore any run-specific scheduling or configuration changes. Follow the instance lifecycle policy and AGENTS.md; never delete a pre-existing environment. The EKS type reserves cluster deletion to the contributor.

## Observations

- Run on `eks-paris` on 2026-10-06–07 with Kasten 9.0.7 and lvmo commit `c6bf41d`: both iSCSI generations exported and restored with the exact expected counts. The NFS full block export also completed through the annotated iSCSI export class. Direct restore to `lvmo-block-nfs` failed attachment because Kasten rebound the NFS target as Block; lvmo returned `NFS requires filesystem access`. The restore was cancelled after diagnosis, and the NFS after-deletion steps were not run. That run did not pass the original scenario, which required a successful NFS round trip. Following contributor review, this document now separates iSCSI recovery from the NFS limitation check; the revision does not retroactively turn the original run into a full pass. Restoring the NFS-source export to an iSCSI application PVC was not tested.
- Record export/restore duration and Kasten's reported bytes read, processed and transferred when available. There is no performance threshold or required 20% reduction in upload size.
- SQL deletion removes logical rows; ordinary VACUUM need not shrink the filesystem or database files. WAL and index changes can affect more blocks than the deleted row range.
- Record whether Kasten consumes CBT only if supported by direct evidence; it is not a pass criterion for this scenario.

## Design notes

- [Kasten block export configuration](https://docs.kasten.io/latest/usage/protect/#exporting-filesystem-volumes-in-block-mode): use `force` to fail rather than fall back, and enable block exports on the participating classes. NFS raw-block access itself remains unsupported.
- [Alternate export StorageClass](https://docs.kasten.io/latest/api/actions/#export-storage-class-example): select the iSCSI clone class on the NFS source class. Both classes must address the same lvmo backend so the snapshot can be cloned through the same CSI driver.
- The alternate export class controls export clone provisioning; it does not redirect the restore target. In the tested Kasten 9.0.7 path, restoring a block export requires raw access that an NFS target cannot supply. An iSCSI Filesystem target would change the application storage protocol and would not meet the NFS recovery requirement.
- Retaining an LV after an iSCSI restore and recreating PV/PVC references for NFS is an unvalidated manual migration, not a supported recovery procedure. It is outside this scenario; do not treat a PVC StorageClass change or LV rebinding as part of a normal restore.
- This scenario validates backup correctness; it neither repeats the EBS performance comparison nor changes the default filesystem-backup path.
