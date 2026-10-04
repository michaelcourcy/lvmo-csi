---
id: performance-aws-baseline
status: manual
groups: [performance]
requires: [kubernetes, storage-server, nfs-client, iscsi-client, aws-storage]
automation: scripts/test-performance.sh
---

# lvmo performance compared with EBS and EFS on the same AWS hardware

## Purpose

Show what lvmo costs or gains compared with AWS's own storage: `lvmo-iscsi` against EBS (both block storage behind a filesystem) and `lvmo-nfs` against EFS (both shared filesystems over NFS). The comparison is only meaningful if lvmo sits on a disk with the same performance as the EBS volumes, so that the difference measured is lvmo's path (network, iSCSI or NFS, LVM thin) and not a different disk. It also checks whether a volume restored from a snapshot reads as fast as its source. An EBS volume restored from a snapshot fetches each block from S3 the first time it is read, so a backup tool such as Kopia that reads a restored or cloned volume once goes much slower than on the original. An lvmo restore is a thin snapshot in the same pool, sharing the source's chunks on the same disk, so it should have no such penalty. Results are added to [docs/performances-test.md](../../docs/performances-test.md).

## Preconditions

- The storage server has a dedicated gp3 EBS data volume with the same provisioned performance as the EBS StorageClass (3000 IOPS, 125 MB/s), holding a VG `lvmo-perf` with a thin pool `lvmo-pool`, served by the API. It is not a loop device. The storage server is not a burstable instance type.
- StorageClasses:
  - `lvmo-perf-iscsi` and `lvmo-perf-nfs` on VG `lvmo-perf`;
  - `ebs-gp3`: EBS CSI driver, `type: gp3`, `iops: "3000"`, `throughput: "125"`;
  - `efs`: EFS CSI driver, dynamic provisioning (`provisioningMode: efs-ap`) on an EFS file system in the cluster's VPC, General Purpose performance mode, Elastic throughput.
- VolumeSnapshotClasses `lvmo-snapshots` (`lvmo.csi.io`, installed by `scripts/install-storageclasses.sh`) and `ebs-snapshots` (`ebs.csi.aws.com`, `deletionPolicy: Delete`), for step 4.
- No other workload runs on the benchmark node.

## Steps

1. Record the instance types of the workers and the storage server, the gp3 settings of the lvmo data volume, and the EFS file system's modes.
2. Run `scripts/test-performance.sh lvmo-perf-iscsi ebs-gp3 lvmo-perf-nfs efs` with the same `NODE` for every class. For each class, it creates a 10Gi RWO PVC and a pod on that node running the same fio jobs, one after another, each for 60 s after a 10 s ramp, with direct I/O on a 4 GiB file:
   - sequential write and read, 1 MiB blocks, queue depth 16;
   - random write and read, 4 KiB blocks, queue depth 32;
   - random read, 4 KiB, queue depth 1, for latency.
3. Restored-volume reads, for `lvmo-perf-iscsi` with `lvmo-snapshots` and for `ebs-gp3` with `ebs-snapshots`, on the same `NODE`:
   1. Create a 20Gi RWO PVC `restore-src`. In a pod mounting it, write a 16 GiB file of incompressible data, so that every block is really allocated and the EBS snapshot holds it: `fio --name=fill --filename=/data/file --size=16g --rw=write --bs=1M --iodepth=16 --ioengine=libaio --direct=1 --refill_buffers --end_fsync=1`. Delete the pod so the filesystem is unmounted cleanly.
   2. Create a VolumeSnapshot of `restore-src` and wait for `readyToUse: true` (timeout 60 min, since a first EBS snapshot of 16 GiB can take tens of minutes). Record the time it took.
   3. Create a 20Gi PVC `restore-clone` from the snapshot. Wait until it is bound.
   4. Read the file once in each of these runs, in this order, each in a new pod so the client's cache starts empty: `restore-src`, then `restore-clone` (first read), then `restore-clone` again (second read). Use one full sequential pass, not a time-based run, because the EBS penalty applies only the first time a block is read: `fio --name=read --filename=/data/file --size=16g --rw=read --bs=1M --iodepth=16 --ioengine=libaio --direct=1 --output-format=json`. For lvmo, run `sync; echo 3 > /proc/sys/vm/drop_caches` on the storage server before each run.
   5. Delete the pods, `restore-clone`, the VolumeSnapshot and `restore-src`. Check that the EBS snapshot is gone from `aws ec2 describe-snapshots`.
4. Add a section to `docs/performances-test.md` with the environment, the table printed by the script, the restored-read table (throughput and elapsed time per run and class, plus the time each snapshot took to be ready), and a short reading of the results.

## Expected

- fio completes for all four classes.
- The three restored-read runs complete for both `lvmo-perf-iscsi` and `ebs-gp3`.
- The report states the environment precisely enough to reproduce the run, and every number in it comes from the fio JSON files.
- The reading compares each lvmo class with its AWS counterpart and names the bottleneck where one is visible (disk limit, network, protocol).
- The reading says, for each class, whether the first read of the restored volume is slower than the read of the original, and whether the second read catches up.

There is no pass threshold: this scenario measures, it does not judge.

## Observations

- What the design predicts, for comparison with the measurement: on `ebs-gp3`, the first read of `restore-clone` is clearly slower than the read of `restore-src`, and the second read is close to it. On `lvmo-perf-iscsi`, all three reads are close, within run-to-run noise, because no data is copied or fetched: each thin device maps directly to the shared chunks, with no chain of snapshots to walk. A measured gap on lvmo would be a finding to investigate.
- On NFS, `direct=1` bypasses the client's cache but not the storage server's: a test file smaller than the server's RAM measures lvmo-nfs reads from memory. Say so in the report, or use a file larger than the server's RAM to measure the disk.

## Validation

Run on `eks-paris` on 2026-10-04; results in [docs/performances-test.md](../../docs/performances-test.md#basic-comparison-on-aws-storage-class). That run covered steps 1, 2 and 4 before step 3 existed; the restored-volume reads have not been run yet.

## Evidence

- The four fio JSON files (`.test/reports/perf/<class>.json`) and the script's table.
- The six restored-read fio JSON files (`.test/reports/perf/restore-<class>-<src|clone1|clone2>.json`) and the snapshot ready times.

## Cleanup

- The script deletes its namespace and PVCs. Step 3.5 deletes the restored-read resources; if the run stopped before it, delete the VolumeSnapshots first so that the EBS snapshot is not left behind and billed. Keep the environment if more measurements are planned.
