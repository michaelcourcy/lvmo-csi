# Kasten performance tests

Each section is one measurement campaign with Kasten by Veeam: what was compared, on which environment, the numbers, and how to read them. The method is the scenario named in each section.

# Exporting a 50 GB PostgreSQL database: lvmo-iscsi against EBS

Run on 2026-10-05 by scenario [kasten-export-performance](../tests/scenarios/kasten-export-performance.md), lvmo at commit `8f085ab`, Kasten 9.0.7.

## Question

The README says that lvmo's thin snapshots and clones are light and quick, and that this shortens Kasten exports. Is that true for a realistic database, compared with EBS, AWS's own block storage? Each export was measured three times: a first full export, a second one without any change, and a third one after deleting 20% of the data.

## Environment

| | |
|---|---|
| Cluster | EKS 1.35, eu-west-3. The databases and Kasten's export pods ran on 2 × m6i.2xlarge in eu-west-3a (non-burstable); the other workers were cordoned |
| lvmo storage server | m6i.xlarge, eu-west-3a; its EBS baseline (156 MB/s) covers its data disk without burst credits |
| lvmo disk | One gp3 volume, **3000 IOPS, 125 MB/s**, VG `lvmo-perf`, thin pool with 64 KiB chunks. 100 GiB at the start, grown to 200 GiB during the deletion step (see [Thin pool space](#thin-pool-space)); gp3 performance does not depend on size |
| `lvmo-perf-iscsi` | lvmo iSCSI, ext4 |
| `ebs-gp3` | EBS CSI driver, gp3, **3000 IOPS, 125 MB/s**: the same disk class as lvmo's |
| Database | `postgres:16`, one per namespace (`pg-lvmo`, `pg-ebs`), 80 GiB PVC, `shared_buffers=2GB`, `max_wal_size=8GB`, each on its own node |
| Data | `pgbench -i -s 3400`: 340 million rows, `pg_database_size` 50 GB (53 320 227 863 bytes), 58 GiB on disk with the WAL |
| Kasten | 9.0.7, its own PVCs on `ebs-gp3`. One on-demand policy per namespace: snapshot, then export to an S3 bucket in eu-west-3 with the regular filesystem export (Kopia). VolumeSnapshotClasses `lvmo-snapshots` and `ebs-snapshots` |

The policies ran one at a time, lvmo first, so that they did not compete for Kasten or for the upload.

Before the run, both databases were loaded in parallel: 23 min 31 s on lvmo-iscsi and 22 min 29 s on EBS, within 5% of each other.

## Results

| Run | | lvmo-iscsi | EBS gp3 | EBS / lvmo |
|---|---|---|---|---|
| **1. Full export** | Snapshot (BackupAction) | 35 s | 65 min 58 s ¹ | |
| | Export (ExportAction) | 8 min 36 s | 46 min 25 s | ×5.4 |
| | **Policy run, total** | **9 min 44 s** | **1 h 52 min 52 s** | **×11.6** |
| | Data added to the bucket | 8.67 GiB | 8.60 GiB | |
| **2. No change** | Snapshot | 5 s | 1 min 19 s | |
| | Export | 25 s | 23 s | |
| | **Policy run, total** | **1 min 00 s** | **2 min 10 s** | **×2.2** |
| | Data added to the bucket | ~0 | ~0 | |
| **3. After deleting 20% of the rows** | Snapshot | 4 s | 26 min 40 s | |
| | Export | 2 min 51 s | 20 min 05 s | ×7.0 |
| | **Policy run, total** | **3 min 13 s** | **47 min 10 s** | **×14.7** |
| | Data added to the bucket | 2.72 GiB | 2.71 GiB | |

¹ Includes two retries: Kasten waits 30 minutes by default for a CSI snapshot to become ready (`executor.csiSnapshotReadyTimeout`), and the first EBS snapshot took longer. The first EBS snapshot alone completed between about 50 and 65 minutes after it started. The timeout was raised to 2 hours before runs 2 and 3.

The 20% deletion in step 3 was `DELETE FROM pgbench_accounts WHERE aid <= 68000000` (a contiguous range, so that about 20% of the table's pages change), then `VACUUM` and `CHECKPOINT`: 9 min 19 s on lvmo-iscsi, 8 min 01 s on EBS. Both databases ended with 272 000 000 rows. All 12 restore points (local and exported, three per namespace) were created, and every action completed.

## Reading

- **The claim holds.** lvmo's policy runs were 11.6 times shorter for the first export and 14.7 times shorter after a 20% change. Kasten uploaded the same amount of data for both (8.6 GiB, then 2.7 GiB after compression and deduplication), so the whole difference comes from the storage: taking the snapshot, and reading it.
- **Snapshots: seconds against minutes or hours.** An lvmo snapshot is a thin snapshot on the storage server: 4 to 35 seconds as seen by Kasten, whatever the amount of data. An EBS snapshot copies the changed blocks to S3 before it is ready: about an hour for the first one (65.8 GiB of blocks), 27 minutes after a 20% change, and still 1 min 19 s with no change at all.
- **Export reads: disk speed against lazy loading.** Kasten exports from a volume restored from the snapshot. On lvmo that volume is a thin clone sharing the snapshot's chunks on the same disk, and the first export read it at about 115 MiB/s, the gp3 disk's limit (58 GiB in 8 min 36 s). A volume restored from an EBS snapshot fetches each block from S3 the first time it is read: the same 58 GiB took 46 minutes, about 21 MiB/s, although its gp3 volume has the same provisioned performance.
- **Without changes, both are quick.** Kopia skips files whose size and modification time did not change, so run 2 read almost nothing. The remaining gap is the EBS snapshot (1 min 19 s against 5 s).
- **After a change, the gap is widest.** Run 3 rereads only the changed files (PostgreSQL's 1 GB segments in the deleted range, the index and the WAL), but on EBS those reads still go through lazy loading, after a snapshot that copies the changed blocks.

## Thin pool space

The lvmo thin pool started at 90 GiB on the 100 GiB disk, 73% used after the load. During the 20% deletion it climbed to 89% in 4 minutes, and the disk was grown online to 200 GiB (`aws ec2 modify-volume`, `pvresize`, `lvextend`) before it filled. Kasten's local snapshots of runs 1 and 2 were still retained, so every page that PostgreSQL rewrote (the deleted rows, `VACUUM`, the index and the reused WAL segments) took new space while the old version stayed in the snapshots: about 18 GiB in that step (65.8 GiB used before, 84.2 GiB after).

This is not specific to Kasten: any retained snapshot keeps the old version of changed data. A thin pool must be sized for the volumes plus the data that changes while snapshots are retained, and monitored (see [Sizing and monitoring the thin pool](../README.md#sizing-and-monitoring-the-thin-pool)). An on-demand policy keeps its local snapshots until they are retired; a scheduled policy with a short local retention limits that growth.

## Limits of this run

- One run per case; no variance measured.
- EBS has faster options that were not used: Fast Snapshot Restore (billed per snapshot and availability zone) removes the lazy loading of restored volumes, and Kasten's block-mode export of EBS volumes uses the EBS Direct API. This compares the regular, default path of both.
- The lvmo server's disk was grown during the deletion step, which overlapped that step's `VACUUM`. It did not overlap any export.
- Retiring the restore points (deleting the RestorePointContents) was not timed.

## Evidence

`.test/reports/kasten-perf/` (not committed): pgbench and SQL timings per namespace, the RunAction, BackupAction and ExportAction of every run with their times, the bucket size after each run, the thin pool usage, the restore points, and the storage server's disk samples.
