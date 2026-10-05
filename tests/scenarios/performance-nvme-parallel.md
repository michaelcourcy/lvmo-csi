---
id: performance-nvme-parallel
status: manual
groups: [performance]
requires: [kubernetes, storage-server, nfs-client, iscsi-client, multi-node]
automation: scripts/test-performance.sh
---

# Twenty parallel workloads on a storage server with one local NVMe disk

## Purpose

[performance-aws-parallel](performance-aws-parallel.md) and [iscsi-overload-graceful](iscsi-overload-graceful.md) ran 20 workloads on a storage server whose disk was one gp3 EBS volume (3000 IOPS, 125 MB/s): that disk was the limit for both lvmo classes. This scenario keeps the same workload and replaces the disk with one local NVMe SSD, as a storage server in a datacenter would have, to see how far lvmo goes when the disk is no longer the obvious bottleneck, and which limit comes next: the disk, the server's network, its CPU (the iSCSI target and the NFS server run in its kernel), or lvmo's path.

On AWS the only local NVMe disks are instance store volumes, which are wiped when the instance stops. This is used here as a stand-in for a datacenter disk that keeps its data; it is a measurement, not a configuration to offer on AWS. Results are added to [docs/performances-test.md](../../docs/performances-test.md), next to the gp3 matrix.

## Preconditions

- The storage server is the gp3 run's server family, with one instance store NVMe disk instead of the gp3 data volume (on `eks`: an `i4i.large`, see [Optional: performance storage on a local NVMe disk](../environments/eks.md#optional-performance-storage-on-a-local-nvme-disk)). The VG `lvmo-nvme` holds the thin pool `lvmo-pool` on that disk alone, created the same way as `lvmo-perf` (thin pool of 90% of the disk, 1 GiB of metadata on the same disk). `fio` and `sysstat` are installed on the server.
- The API and the driver include the iSCSI queue bound (`--iscsi-queue-depth`, default 8), so that the iSCSI result is comparable with the bounded gp3 run.
- StorageClasses `lvmo-nvme-iscsi` and `lvmo-nvme-nfs`, on VG `lvmo-nvme`.
- The client nodes of the gp3 run: two m6i.2xlarge in the storage server's availability zone, labelled `lvmo-bench=true` and tainted so that nothing else runs there.

## Steps

1. Record the storage server's instance type, its vCPUs and RAM, its network baseline and burst bandwidth (`aws ec2 describe-instance-types`), and the NVMe disk's model and size (`lsblk -d -o NAME,MODEL,SIZE`).
2. Before creating the VG, measure the raw disk on the server with fio (`direct=1`, `libaio`, 30 s each): sequential write and read with 1 MiB blocks at queue depth 32, random write and read with 4 KiB blocks at queue depth 128 with 4 jobs, and random read 4 KiB at queue depth 1. This gives the disk's own limits, without the network or lvmo.
3. Run one workload per class: `NODE_LABEL=kubernetes.io/hostname=<a bench node> scripts/test-performance.sh lvmo-nvme-iscsi lvmo-nvme-nfs`, with `OUT=.test/reports/perf-nvme-single`.
4. On the storage server, record the network interface's allowance counters (`ethtool -S ens5 | grep allowance`), and start sampling every 10 seconds with `iostat -dxmt <nvme disk> 10` and `mpstat 10`.
5. Record the time, then run `REPLICAS=20 FILE_SIZE=2g PREFILL_WINDOW=900 NODE_LABEL=lvmo-bench=true scripts/test-performance.sh lvmo-nvme-iscsi lvmo-nvme-nfs`, with `OUT=.test/reports/perf-nvme`. The 15-minute prefill window covers writing the 40 GiB even at the server's network baseline; a longer idle wait would only refill its network burst credits.
6. Stop the samplers, record the allowance counters again, and count `ABORT_TASK`, `DataOut timeout` and `closing iSCSI connection` messages in the server's kernel log since the recorded time.
7. Add a section to `docs/performances-test.md`: the environment, the raw disk figures, the single and 20-workload tables, the comparison with the gp3 matrix, and the limit each class reached.

## Expected

- fio completes in all 40 pods of step 5, without errors, and the target logs no abort, data timeout or closed connection.
- The report explains each total by a limit observed in the evidence: the disk (iostat utilisation and the raw figures of step 2), the server's network (allowance counters that grew during the run), its CPU (mpstat), or the clients; or it says that none was found.
- The report says whether the per-pod shares stay even (sequential write per pod within a factor of 2 of the median), as on gp3.

There is no pass threshold: this scenario measures, it does not judge.

## Observations

- The server has more RAM than the gp3 run's m6i.large (16 GiB against 8 GiB), and the 20 files total 40 GiB: lvmo-nfs reads partly come from the server's page cache. Say how much, from the read throughput against the disk's.
- The i4i.large network baseline is 0.78 Gbit/s, with bursts up to 10 Gbit/s. A total above the baseline relies on burst credits; when they run out, the `bw_in_allowance_exceeded` and `bw_out_allowance_exceeded` counters grow and throughput drops.
- Whether the small writes seen on new gp3 volumes during prefill (1 to 5 KiB, suspected to be ext4's lazy inode table initialization) still slow the prefill on NVMe.

## Validation

Run on `eks-paris` on 2026-10-05 (i4i.large, lvmo commit `8f085ab`); results in [docs/performances-test.md](../../docs/performances-test.md#twenty-parallel-workloads-with-one-local-nvme-disk). 40/40 pods completed with no errors and no target abort. Every lvmo-iscsi total equalled the raw disk's limit; lvmo-nfs matched it except for random writes (1.5 disk writes per client write). Random reads also used most of the server's 2 vCPU. The run showed that new iSCSI volumes send millions of 1 KiB writes after their first mount (ext4 lazy inode table initialization, emulated by LIO on a thin LV without write-zeroes support).

## Evidence

- Instance and disk limits, the raw fio figures, `.test/reports/perf-nvme-single/<class>.json`, `.test/reports/perf-nvme/<class>/<n>.json`, the script's tables, the iostat, mpstat and allowance samples, and the kernel log counts.

## Cleanup

- The script deletes its namespace and PVCs. Stop the samplers. The instance store disk is wiped when the storage server is terminated.
