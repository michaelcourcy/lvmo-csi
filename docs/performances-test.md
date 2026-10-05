# Performance tests

Each section is one measurement campaign: what was compared, on which environment, the numbers, and how to read them. Numbers come from fio JSON output; the method is the scenario named in each section.

# Basic comparison on aws storage class

Run on 2026-10-04 by scenario [performance-aws-baseline](../tests/scenarios/performance-aws-baseline.md) with `scripts/test-performance.sh`, lvmo `v0.1.0-alpha.3`.

## Question

How do lvmo's two protocols compare with AWS's own storage on the same hardware: `lvmo-iscsi` against EBS, both block devices behind a filesystem, and `lvmo-nfs` against EFS, both shared filesystems over NFS?

## Environment

| | |
|---|---|
| Cluster | EKS 1.35, eu-west-3, 3 × t3.medium workers (Ubuntu 24.04) |
| Benchmark node | `ip-192-168-93-132`, eu-west-3a, used for every run |
| lvmo storage server | m6i.large (2 vCPU, 7 GiB usable RAM), eu-west-3a, same zone as the benchmark node |
| lvmo disk | Dedicated gp3 EBS volume, 100 GiB, **3000 IOPS, 125 MB/s**, used directly as the LVM physical volume (no loop file); VG `lvmo-perf`, thin pool, ext4 on each thin LV |
| `lvmo-perf-iscsi` | lvmo iSCSI, ext4 formatted by lvmo, mounted on the node |
| `ebs-gp3` | EBS CSI driver, gp3, **3000 IOPS, 125 MB/s**: the same disk class as lvmo's, attached to the node |
| `lvmo-perf-nfs` | lvmo NFS v4.1: ext4 on the storage server, exported over NFS |
| `efs` | EFS CSI driver, access point on an EFS file system, General Purpose, Elastic throughput |

**Method:** one 10 GiB RWO PVC per class, one pod running fio 3.36 on a 4 GiB file, `direct=1`, `libaio`. Five jobs run one after another, each for 60 s after a 10 s ramp-up:
- sequential write and read, 1 MiB blocks, queue depth 16;
- random write and read, 4 KiB blocks, queue depth 32;
- random read, 4 KiB, queue depth 1, which measures latency.

## Results

| StorageClass | Seq write 1M (MiB/s) | Seq read 1M (MiB/s) | Rand write 4k (IOPS) | Rand read 4k (IOPS) | Rand read 4k QD1 p50 / p99 (µs) |
|---|---|---|---|---|---|
| lvmo-perf-iscsi | 125 | 125 | 2931 | 2976 | 750 / 1106 |
| ebs-gp3 | 125.3 | 125.3 | 2999 | 2999 | 586 / 1073 |
| lvmo-perf-nfs | 124.9 | 589.6 ¹ | 2399 | 50367 ¹ | 157 / 354 ¹ |
| efs | 234.3 | 485.4 | 2871 | 15995 | 627 / 1499 |

¹ Served from the storage server's page cache, not from its disk: see below.

## Reading

**lvmo-iscsi against EBS: the same throughput and IOPS, slightly higher latency.** Both reach the gp3 volume's provisioned limits (125 MB/s, 3000 IOPS) on every throughput test: the disk is the bottleneck for both, and lvmo-iscsi loses 2% or less at saturation. The cost of the extra hop shows in single-request latency. A 4 KiB read at queue depth 1 takes 750 µs median through lvmo, against 586 µs for EBS directly: about 165 µs more for the network to the storage server and the iSCSI target, while the tail (p99) is almost identical, 1.1 ms for both. With a faster disk on the storage server, lvmo-iscsi would scale until the server's network or CPU limits, where EBS scales with the volume's provisioned performance.

**lvmo-nfs against EFS: lvmo is faster when data fits in the storage server's memory, EFS writes more.**
- **Reads.** lvmo-nfs reads (590 MiB/s, 50k IOPS, 157 µs) are far above what the gp3 disk can deliver. `direct=1` bypasses the client's cache, but the NFS server keeps the file in its own page cache, and the 4 GiB test file fits in the server's 7 GiB of RAM. These numbers therefore measure the server's memory and network, not its disk. They are real for a working set that fits in the server's RAM, which is common for small volumes, but they are not a disk-to-disk comparison. EFS, a distributed service, delivered 485 MiB/s and 16k IOPS at 627 µs: lvmo-nfs is 3× more IOPS and 4× lower latency when cached.
- **Writes.** lvmo-nfs writes go to the gp3 disk: 125 MiB/s, its limit. EFS with Elastic throughput is not bound to one disk and writes almost twice as fast (234 MiB/s). Random 4k writes are comparable (2399 IOPS for lvmo-nfs, 2871 for EFS); lvmo-nfs stays below its own disk's 3000 IOPS because of the NFS and server-side ext4 overhead on synchronous small writes.

**Summary.** lvmo adds little to the disk under it: with the same disk as EBS it performs like EBS, at a small latency cost. Its performance is that of the storage server's disk, RAM and network, which you choose, whereas EBS and EFS scale with provisioned or elastic AWS capacity.

## Limits of this run

- One run per class, one client: no variance measured and no concurrent clients. Concurrency would favour EFS, which scales out, while lvmo shares one server.
- The working set (4 GiB) fits in the lvmo storage server's RAM, so lvmo-nfs reads are cached. A disk-to-disk NFS comparison needs a file larger than the server's RAM, or the server's page cache dropped before each read job.
- The t3.medium client and the m6i.large server have "up to" network bandwidth; a longer or heavier run could hit their limits first.
- EFS Elastic throughput is billed per GB transferred; EBS and lvmo performance depend on the gp3 settings chosen here.

## Evidence

The fio JSON outputs (`.test/reports/perf/<class>.json`, not committed) and `scripts/test-performance.sh`'s table above.

# Twenty parallel workloads on aws storage class

Run on 2026-10-04 by scenario [performance-aws-parallel](../tests/scenarios/performance-aws-parallel.md) with `scripts/test-performance.sh`, lvmo `v0.1.0-alpha.3`.

## Question

The baseline measured one workload at a time. What happens with 20 at once, each on its own volume? The expectation was that AWS storage scales better: each EBS volume brings its own provisioned performance and EFS is a distributed service, while lvmo serves every volume from one storage server and one disk. This comparison is deliberately unequal in hardware (20 gp3 volumes against one); it asks how each side behaves when the load exceeds what one disk can do.

## Environment

As in the baseline, with these differences:

| | |
|---|---|
| Clients | 2 × m6i.2xlarge (8 vCPU, non-burstable), eu-west-3a, a temporary node group used only by the benchmark; 10 pods on each. Per node: EBS up to 1250 MB/s and 40 000 IOPS, network up to 12.5 Gbit/s. |
| lvmo storage server | m6i.large, same zone, same dedicated gp3 data disk (3000 IOPS, 125 MB/s) |
| Workload | 20 pods per class, each with its own 10 GiB PVC and a 2 GiB file. Each pod first writes its whole file (prefill, not measured); then all 20 start the same five jobs as the baseline at the same moment (start spread ≤ 1.1 s for the classes that completed). |

## Results

Totals are the sum over the 20 pods of the bytes or operations each completed, divided by its job runtime.

| StorageClass | Pods OK | Seq write 1M, total (MiB/s) | Seq read 1M, total (MiB/s) | Rand write 4k, total (IOPS) | Rand read 4k, total (IOPS) | Rand read 4k QD1 p50, median pod (µs) |
|---|---|---|---|---|---|---|
| ebs-gp3 | 20/20 | 2 392 | 2 396 | 59 981 | 59 989 | 545 |
| efs | 20/20 | 1 034 | 2 967 | 32 683 | 128 604 | 569 |
| lvmo-perf-nfs | 20/20 | 124 | 175 | 997 | 3 462 | 5 997 |
| lvmo-perf-iscsi | 12/20 | collapsed: see below | | | | |

Compared with one workload (baseline), the totals scale by:

| | Seq write | Seq read | Rand write 4k | Rand read 4k |
|---|---|---|---|---|
| ebs-gp3 | ×19 | ×19 | ×20 | ×20 |
| efs | ×4.4 | ×6.1 | ×11 | ×8.0 |
| lvmo-perf-nfs | ×1.0 | — ¹ | ×0.4 | — ¹ |

¹ The baseline's lvmo-nfs reads came from the server's page cache (4 GiB file, 7 GiB of RAM); here 40 GiB of data does not fit, so reads are mostly from the disk and the ratio would be misleading.

## Reading

**EBS scales linearly.** Every pod got its volume's full 3000 IOPS and about 119 MiB/s, and the 20 volumes together delivered 2.4 GiB/s. That total is also exactly the two client nodes' EBS bandwidth (2 × 1250 MB/s): with more volumes per node, the client instance would be the next limit, not EBS. Latency did not change (545 µs at queue depth 1).

**EFS scales, up to its file system limits.** About 1 GiB/s of writes and 3 GiB/s of reads in total, consistent with the documented per-file-system limits of Elastic throughput. Per-pod reads were uneven (31 to 444 MiB/s), but nothing failed and median latency stayed at 569 µs.

**lvmo-nfs stays at its disk's limit, shares it fairly, and does not fail.** 124 MiB/s of writes in total is the gp3 disk's 125 MB/s, split evenly (6.2 MiB/s per pod). Random 4k writes reach a third of the disk's IOPS: each NFS write is synchronous on the server, with ext4 journalling and thin-pool allocation behind it. Reads slightly exceed the disk (175 MiB/s, 3462 IOPS) thanks to the server's page cache. Latency rises from 157 µs to 6 ms as 20 clients queue on one disk. This is graceful degradation: slower, fair, and without errors.

**lvmo-iscsi collapsed.** On the same disk, the 20 prefills reached it as about 3000 operations per second of ~5 KiB each: 13–18 MB/s instead of 125. The prefill that should take 5.5 minutes would have needed about 45; the pods fell out of step, some commands waited long enough for the clients to abort them (`ABORT_TASK` on the target), and 8 of the 20 pods were still stuck after 50 minutes. An earlier attempt also had an iSCSI connection closed by the target after a data timeout, and one pod's I/O failed. The disk's EBS and I/O credit balances stayed at 99–100% and its CPU low: the disk's operation count was the limit. Suspected cause, not proven yet: the LVM thin pool's metadata, on the same disk, turning each filesystem's journal commits into small metadata writes. lvmo-nfs avoids this because the NFS server's page cache coalesces writes before they reach the thin volumes.

**Summary.** For total capacity, the expectation holds: EBS scales with the number of volumes and EFS with the service, while lvmo is bound to its server's disk. The unexpected result is that lvmo's two protocols react very differently to overload on the same disk: NFS degrades gracefully, iSCSI does not. Making lvmo-iscsi degrade gracefully is the next priority, before cost optimization.

## How this run was obtained

Two earlier attempts were discarded, and their evidence kept:

1. Without prefill, files were created with `fallocate` and only partly written during the test; reads of never-written blocks return zeros without disk access (on thin LVs and new EBS volumes alike), giving an impossible 1.8 GB/s from a 125 MB/s disk. Each pod now writes its whole file first, then all pods wait for a common start.
2. The lvmo-iscsi class timed out as described above, and was not rerun; the other three classes ran separately.

The run was orchestrated from a laptop: when it lost network during the EFS class, the 20 pods had already completed in the cluster, and their results were collected afterwards.

## Limits of this run

- One run per class; no variance measured.
- The lvmo storage server has one gp3 disk of 3000 IOPS and 125 MB/s, far below the 20 EBS volumes' combined 60 000 IOPS and 2.5 GB/s: this compares architectures under overload, not equal budgets. An equal-budget or equal-capacity comparison is a separate test.
- EBS totals reached the client nodes' EBS bandwidth; more client nodes would show EBS higher still.

## Evidence

`.test/reports/perf-parallel/<class>/<n>.json` (fio output per pod), `.test/reports/perf-parallel-attempt3-lvmo-iscsi-timeout/iostat-nvme1n1.txt` (the lvmo disk during the collapse), and the earlier attempts in `.test/reports/perf-parallel-attempt*` (not committed).

# Bounding iSCSI queues so that an overloaded disk degrades gracefully

Run on 2026-10-04 and 2026-10-05 on the environment of the parallel test (one gp3 disk of 3000 IOPS and 125 MB/s behind the lvmo storage server, 2 × m6i.2xlarge clients), with scenario [iscsi-overload-graceful](../tests/scenarios/iscsi-overload-graceful.md).

## Question

In the parallel test, 20 lvmo-iscsi workloads on one disk did not just slow down: commands were aborted and pods hung. Can bounding the number of commands each volume keeps in flight turn that collapse into an even slowdown?

## What changed

Each iSCSI session used to keep up to 32 commands in flight from the node, with a target window of 64 or more. On an overloaded disk, the queue of 20 sessions waited longer than the client's 30-second SCSI timeout: commands were aborted and resent, adding load, and connections closed. lvmo now bounds each volume to 8 commands in flight, on the target (the portal group's `default_cmdsn_depth`, inherited by its ACLs) and on the node (`node.session.queue_depth`, with `cmds_max` derived from it), and sets a 120-second SCSI timeout on the node's disk. Both are API flags: `--iscsi-queue-depth` and `--iscsi-command-timeout`.

## Results

Same workload as the parallel test: 20 pods, each with its own 10 GiB volume and 2 GiB file, prefill then the five jobs together.

| lvmo-perf-iscsi, 20 workloads | Default queues | Bound, set by hand (experiment) | Bound, set by lvmo (implementation) |
|---|---|---|---|
| Pods completed | 12/20, 8 stuck | 20/20 | **20/20** |
| `ABORT_TASK` / data timeouts / closed connections | yes | 0 / 0 / 0 | **0 / 0 / 0** |
| Seq write 1M, total | — | 127 MiB/s | **128 MiB/s** |
| Seq read 1M, total | — | 129 MiB/s | **130 MiB/s** |
| Rand write 4k, total | — | 2 845 IOPS | **2 991 IOPS** |
| Rand read 4k, total | — | 3 777 IOPS | **3 015 IOPS** |
| Seq write per pod, min / median / max | — | 5.5 / 6.5 / 7.0 MiB/s | **5.9 / 6.5 / 6.8 MiB/s** |
| Rand read 4k QD1, median pod p50 | — | 9.1 ms | **6.7 ms** |
| Worst pod p99 latency | — | 7.4 s (seq write) | **4.9 s** (seq write) |

## Reading

With the bound, the same overload is absorbed: every pod completes, the target logs no abort or timeout, the totals sit at the disk's limits (125 MB/s, 3000 IOPS), and the disk is shared evenly between the 20 volumes. The worst wait, under 5 seconds, is far from the 120-second timeout. lvmo-iscsi now degrades the way lvmo-nfs did in the parallel test: slower, fair, and without errors.

The bound does not change what the disk can do with small writes. During the prefill of 20 new volumes, the disk still spent its 3000 operations per second on writes of 1 to 5 KiB for about 25 minutes, before reaching its full 125 MB/s once those stopped. They come from each new volume shortly after its first mount, and stop by themselves: ext4's lazy initialization of inode tables, done by a kernel thread after the first mount, is the first suspect, with the thin pool's metadata on the same disk. That is the next investigation, aimed at throughput rather than resilience.

## Evidence

`.test/reports/perf-queue8/` (experiment) and `.test/reports/perf-queue-impl/` (implementation): fio output per pod, per-pod fairness, the target's kernel log counts, the bound as seen on the target and the node, and the data disk sampled every 10 seconds (not committed).

# Final matrix: twenty parallel workloads, with lvmo-iscsi bounded

This section brings together the 20-workload results of the four StorageClasses, with lvmo-iscsi running with the queue bound (`--iscsi-queue-depth 8`, `--iscsi-command-timeout 2m`, the defaults). The sections above remain the detailed record of each run.

## Conditions

- Same workload for every class: 20 pods, each with its own 10 GiB volume and a 2 GiB file, written in full first (prefill), then the five fio jobs started together.
- Same kind of clients: 2 × m6i.2xlarge in eu-west-3a, 10 pods on each. The lvmo-iscsi run used a recreated node group of the same type and zone.
- The lvmo classes share one m6i.large storage server with one gp3 data disk (3000 IOPS, 125 MB/s). Each EBS volume has its own 3000 IOPS and 125 MB/s.
- The queue bound only concerns iSCSI: `ebs-gp3`, `efs` and `lvmo-perf-nfs` come from the run described in [Twenty parallel workloads](#twenty-parallel-workloads-on-aws-storage-class), `lvmo-perf-iscsi` from [the implementation run](#bounding-iscsi-queues-so-that-an-overloaded-disk-degrades-gracefully).

Every number below is computed the same way from the fio output of each pod: a total is the sum over the 20 pods of bytes or operations divided by each job's runtime; "worst p99" is the highest p99 latency of any pod in any of the four throughput jobs.

## Matrix

| | ebs-gp3 | efs | lvmo-perf-nfs | lvmo-perf-iscsi (bounded) |
|---|---|---|---|---|
| Pods completed, fio errors | 20/20, 0 | 20/20, 0 | 20/20, 0 | 20/20, 0 |
| Seq write 1M, total (MiB/s) | 2 392 | 1 034 | 124 | 128 |
| Seq read 1M, total (MiB/s) | 2 396 | 2 967 | 175 | 130 |
| Rand write 4k, total (IOPS) | 59 981 | 32 683 | 997 | 2 991 |
| Rand read 4k, total (IOPS) | 59 989 | 128 604 | 3 462 | 3 015 |
| Seq write per pod, min – max (MiB/s) | 118.9 – 120.8 | 51.6 – 52.3 | 6.2 – 6.3 | 5.9 – 6.8 |
| Rand read 4k QD1, median pod p50 (µs) | 545 | 569 | 5 997 | 6 652 |
| Rand read 4k QD1, worst pod p99 (µs) | 872 | 6 521 | 8 716 | 8 094 |
| Worst p99, any throughput job (ms) | 184 | 801 | 4 530 | 7 550 |
| What limits the total | Each volume's 3000 IOPS / 125 MB/s, and the clients' EBS bandwidth (2 × 1250 MB/s) | EFS Elastic throughput (about 1 GiB/s write, 3 GiB/s read) | The storage server's one gp3 disk | The storage server's one gp3 disk |

## Reading

- **Capacity**: EBS and EFS scale with the number of workloads; both lvmo classes stay at what one disk delivers (about 125 MB/s and 3000 IOPS). This is the expected consequence of comparing 20 disks, or a distributed service, with one disk behind one server.
- **Behaviour under overload**: with the bound, all four classes complete every workload without errors, and the lvmo classes share their disk evenly (sequential writes of 5.9 to 6.8 MiB/s per pod on iSCSI, 6.2 to 6.3 on NFS). Without the bound, lvmo-iscsi hung 8 of 20 pods in the same test.
- **lvmo-iscsi against lvmo-nfs, on the same disk**: iSCSI now reaches the disk's IOPS for random writes (2 991 against 997 for NFS, whose writes are synchronous on the server), while NFS reads slightly more thanks to the server's page cache (175 MiB/s and 3 462 IOPS against 130 MiB/s and 3 015 IOPS). Latencies are of the same order: 6 to 7 ms for a single read with 20 clients queued on one disk.
- **Latency**: EBS keeps sub-millisecond single reads under load; EFS keeps the median but has a longer tail; lvmo pays the queueing on its one disk, bounded well below the 2-minute command timeout.

The next steps are lvmo's throughput on new volumes (small writes during the first minutes, suspected to be ext4's lazy initialization) and cost: a storage server disk sized for the workloads, compared on equal budget with EBS.
