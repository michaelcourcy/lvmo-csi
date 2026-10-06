---
id: kasten-export-performance
status: manual
groups: [backup, performance]
requires: [kubernetes, storage-server, iscsi-client, kasten, object-storage, aws-storage]
automation: none
---

# Kasten export time of a 50 GB PostgreSQL database: lvmo-iscsi against EBS

## Purpose

The README says that lvmo's thin snapshots and clones are light and quick, which shortens Kasten exports. This scenario checks that claim on a realistic workload. The same 50 GB PostgreSQL database (pgbench) lives once on an lvmo-iscsi volume and once on an EBS gp3 volume with the same provisioned performance. Each namespace has its own Kasten policy, which snapshots the volume and exports it to S3. The policies run three times: a first full export, a second one without any change, and a third one after about 20% of the data was deleted.

An EBS export reads a volume restored from an EBS snapshot, and such a volume fetches every block from S3 the first time it is read. An lvmo export reads a thin clone sharing its chunks with the source, on the same disk. The expected difference is therefore in the snapshot and in the reading of changed data, not in the upload.

Exports use Kasten's regular filesystem mode (Kopia), which skips unchanged files and deduplicates changed ones. Block-mode exports and changed block tracking (KEP-3314) are out of scope. Results go to [docs/kasten-performance-test.md](../../docs/kasten-performance-test.md).

## Preconditions

- Storage server with a dedicated gp3 data volume of 100 GiB, 3000 IOPS and 125 MB/s, holding VG `lvmo-perf` with its thin pool, on a non-burstable instance whose EBS and network baselines cover that disk (on `eks`: m6i.xlarge, EBS baseline 156 MB/s). The API and the driver include the iSCSI queue bound.
- StorageClasses:
  - `lvmo-perf-iscsi` (`protocol: iscsi`, `vg: lvmo-perf`);
  - `ebs-gp3` (`ebs.csi.aws.com`, `type: gp3`, `iops: "3000"`, `throughput: "125"`, `WaitForFirstConsumer`).
- VolumeSnapshotClasses `lvmo-snapshots` and `ebs-snapshots`, both annotated `k10.kasten.io/is-snapshot-class: "true"`.
- Kasten installed in `kasten-io`, its own PVCs on `ebs-gp3` so that they do not load the lvmo storage server, and a validated S3 location profile `kasten-perf-s3` for a private bucket in the cluster's region, with credentials limited to that bucket.
- Two non-burstable worker nodes (on `eks`: m6i.2xlarge) in the storage server's availability zone. During the run, no other node takes new pods (cordon the others), so that the databases and Kasten's export pods run on them.

## Steps

1. Create namespaces `pg-lvmo` and `pg-ebs`. In each, create an 80Gi RWO PVC `data` (`lvmo-perf-iscsi` and `ebs-gp3` respectively) and a Deployment running `postgres:16` with `PGDATA` on it, `shared_buffers=2GB`, `max_wal_size=8GB`, requests of 2 CPU and 8 GiB, each database pinned to a different benchmark node.
2. In both databases at the same time, run `pgbench -i -s 3400 -I dtgvp` (client-side generation, which loads with `COPY FREEZE`, so that no later vacuum rewrites the table to freeze it). Then run `CHECKPOINT`. Record the duration, `pg_database_size`, and the row count of `pgbench_accounts`.
3. Create two Kasten policies, `pg-lvmo-export` and `pg-ebs-export`, on demand (`frequency: "@onDemand"`), each selecting its namespace, taking a snapshot and exporting it to `kasten-perf-s3` (filesystem export).
4. **Run 1, full export**: run `pg-lvmo-export`, wait for it to complete, then run `pg-ebs-export` and wait (one at a time, so that they do not compete for the S3 upload or Kasten's resources). For each run, record the start and end times of the RunAction, the BackupAction (snapshot) and the ExportAction, and the size added to the policy's Kopia repository in the bucket.
5. **Run 2, no change**: run both policies again in the same order without touching the databases, and record the same values.
6. **Change about 20% of the data**: in both databases, `DELETE FROM pgbench_accounts WHERE aid <= 68000000` (20% of the 340 million rows, a contiguous range), then `VACUUM pgbench_accounts` and `CHECKPOINT`. Record the duration, `pg_database_size` and the new row count.
7. **Run 3, after the change**: run both policies again in the same order and record the same values.
8. Check that the restore points of every run exist for both namespaces, local and exported.
9. Write `docs/kasten-performance-test.md` with the environment, the timings of each phase for each run and each storage, the data exported per run, and whether the README's claim holds.

## Expected

- Every RunAction, BackupAction and ExportAction completes without error, and each run yields a local and an exported restore point for both namespaces.
- Both databases have the same row counts after step 2 (340 000 000) and after step 6 (272 000 000).
- The report explains each duration by what was observed: snapshot creation, restored volume creation, data read, data uploaded.
- The report says whether the README's claim holds: whether lvmo's snapshot and export phases are shorter than EBS's, and by how much in each of the three runs.

There is no pass threshold: this scenario measures, it does not judge.

## Observations

- Whether the EBS snapshot of run 1 (a full snapshot of 50 GB) dominates its run time, and how much runs 2 and 3 gain from EBS's incremental snapshots.
- How much of each export reads data again: Kopia rereads every file whose size or modification time changed, here PostgreSQL's 1 GB segment files of the deleted range plus the WAL and the index.
- The thin pool's usage on the storage server after each run, since retained snapshots keep the old version of changed chunks.

## Validation

Run on `eks-paris` on 2026-10-05 with Kasten 9.0.7; results in [docs/kasten-performance-test.md](../../docs/kasten-performance-test.md). Every action completed. Policy run totals, lvmo-iscsi against EBS: 9 min 44 s against 1 h 52 min 52 s for the full export, 1 min 00 s against 2 min 10 s without change, 3 min 13 s against 47 min 10 s after the 20% deletion, with the same data uploaded. Two deviations: Kasten's 30-minute CSI snapshot ready timeout made the first EBS snapshot retry twice (raised to 2 h for runs 2 and 3), and the 100 GiB lvmo disk was grown online to 200 GiB during the deletion, because retained snapshots made the thin pool fill.

## Evidence

- Node, storage server and Kasten versions; pgbench and SQL timings; RunAction, BackupAction and ExportAction statuses with their times; bucket sizes per repository after each run; restore points; thin pool usage.

## Cleanup

- Delete both policies, then retire their restore points: delete the RestorePoints and then their RestorePointContents (`-l k10.kasten.io/appNamespace in (pg-lvmo,pg-ebs)`); deleting a RestorePointContent is what makes Kasten retire the snapshots and the exported data. Wait for the RetireActions to complete, then delete namespaces `pg-lvmo` and `pg-ebs`, then check that no lvmo volume or snapshot remains on the storage server and no EBS snapshot is left with the cluster's tags. Delete the location profile, the bucket and its credentials if the run created them, and uncordon the nodes.
