# Performance tests

Each section is one measurement campaign: what was compared, on which environment, the numbers, and how to read them. Numbers come from fio JSON output; the method is the scenario named in each section.

## Summary: lvmo delivers what the storage server's disks can do

**lvmo delivers what the storage server's disks can do, shared fairly between volumes. Size the server's disks, CPU and network, and you know what the workloads will get.**

Three storage server topologies ran the same test: 20 workloads at once, each on its own lvmo-iscsi volume. Each time, the totals landed on the limits of the server's disks, measured with fio directly on the server:

| Storage server's disks | Raw disks: seq write, rand write, rand read | lvmo-iscsi, 20 workloads: seq write, rand write, rand read | Section |
|---|---|---|---|
| 1 gp3 volume | 125 MB/s, 3 000 IOPS, 3 000 IOPS (provisioned) | 128 MiB/s, 2 991 IOPS, 3 015 IOPS | [gp3, bounded](#bounding-iscsi-queues-so-that-an-overloaded-disk-degrades-gracefully) |
| 1 local NVMe disk | 263 MiB/s, 27 494 IOPS, 49 991 IOPS | 268 MiB/s, 27 545 IOPS, 50 096 IOPS | [NVMe](#twenty-parallel-workloads-with-one-local-nvme-disk) |
| 5 gp3 volumes, striped | 625 MiB/s, 14 929 IOPS, 14 983 IOPS | 629 MiB/s, 14 990 IOPS, 14 976 IOPS | [Striped](#twenty-parallel-workloads-with-five-striped-gp3-volumes) |

lvmo itself never became the limit, up to 630 MiB/s and 50 000 IOPS. Each workload gets an even share of the total: sequential writes per volume stayed within a few percent of each other in every run, and no workload failed.

This holds under these conditions:

- **The server's CPU, network and RAM count as well as its disks.** The iSCSI target and the NFS server run in its kernel: a 2-vCPU server was close to CPU-bound at 50 000 IOPS. On a server with more RAM than data, NFS reads can exceed the disks thanks to the page cache.
- **NFS needs enough server threads.** With Ubuntu's default of 8 NFS server threads, NFS random I/O stopped far below disks of ordinary latency (2 895 IOPS on the striped volumes); with 64 it reached them (8 626). `scripts/setup-vm.sh` does not set this yet.
- **NFS random writes cost more than iSCSI's**: 1.5 to 3 disk writes per client write (ext4's journal and metadata on the server), so NFS reaches a third to two thirds of iSCSI's random write IOPS on the same disks (33% on one gp3 volume, 58% on the stripe, 65% on NVMe).
- **New iSCSI volumes generate millions of 1 KiB writes during their first minutes** (ext4 lazy initialization, emulated by the iSCSI target), taking the disks' IOPS from other volumes until it ends. A fix is proposed, not implemented yet.
- **Latency**: lvmo adds about 100 µs per operation over the raw disk; under load, latency is the queueing of all volumes on the shared disks.
- **This depends on the iSCSI queue bound** (`--iscsi-queue-depth`, since commit `e8c064b`). Without it, 20 iSCSI workloads on one overloaded disk collapsed instead of slowing down.
- **What was not tested**: more than 630 MiB/s or 50 000 IOPS, real applications instead of fio, variance between runs (one run each), and on-premises hardware. The NVMe and striped servers were AWS stand-ins for on-premises servers.

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

# Twenty parallel workloads with one local NVMe disk

Run on 2026-10-05 by scenario [performance-nvme-parallel](../tests/scenarios/performance-nvme-parallel.md) with `scripts/test-performance.sh`, lvmo at commit `8f085ab` (queue bound included, `--iscsi-queue-depth 8`).

## Question

In the gp3 runs above, both lvmo classes stopped at what one gp3 volume delivers (125 MB/s, 3000 IOPS). What happens with the same workload when the storage server has one local NVMe SSD instead, as a server in a datacenter would? How far does lvmo go, and which limit comes next?

On AWS the only local NVMe disks are instance store volumes, which are wiped when the instance stops. Here one stands in for a datacenter disk that keeps its data. **This is a measurement, not a way to run lvmo on AWS.**

## Environment

As in the gp3 runs (EKS 1.35 in eu-west-3, 2 × m6i.2xlarge clients in eu-west-3a, 10 pods on each), with these differences:

| | |
|---|---|
| lvmo storage server | **i4i.large**, eu-west-3a: 2 vCPU like the m6i.large of the gp3 runs, 16 GiB of RAM (against 8), network baseline 0.78 Gbit/s with bursts up to 10 Gbit/s |
| lvmo disk | **one 468 GB NVMe instance store disk** (436 GiB usable), used directly as the LVM physical volume; VG `lvmo-nvme`, thin pool of 392 GiB with the same 64 KiB chunks and 1 GiB of metadata on the same disk as `lvmo-perf` |
| Classes | `lvmo-nvme-iscsi`, `lvmo-nvme-nfs`. EBS and EFS were not rerun: their figures come from the gp3 runs |
| Workload | Unchanged: 20 pods per class, each with its own 10 GiB volume and a 2 GiB file, written in full first, then the five jobs started together (start spread ≤ 1 s) |

## The raw disk

fio on the raw device on the server, before the VG was created: no network, no LVM, no filesystem. These are the limits lvmo can reach at best.

| | Seq write 1M | Seq read 1M | Rand write 4k | Rand read 4k | Rand read 4k QD1 p50 / p99 |
|---|---|---|---|---|---|
| NVMe instance store (i4i.large) | 263 MiB/s | 334 MiB/s | 27 494 IOPS | 49 991 IOPS | 117 / 126 µs |
| gp3 volume of the earlier runs (provisioned) | 125 MB/s | 125 MB/s | 3 000 IOPS | 3 000 IOPS | — |

The NVMe disk delivers about 2 to 2.7 times gp3's throughput and 9 to 17 times its IOPS.

## One workload

| StorageClass | Seq write 1M (MiB/s) | Seq read 1M (MiB/s) | Rand write 4k (IOPS) | Rand read 4k (IOPS) | Rand read 4k QD1 p50 / p99 (µs) |
|---|---|---|---|---|---|
| lvmo-nvme-iscsi | 261.6 | 334.6 | 27 542 | 29 684 | 224 / 289 |
| lvmo-nvme-nfs | 262.1 | 591.7 ¹ | 23 948 | 70 403 ¹ | 121 / 163 ¹ |

¹ From the server's page cache: the 4 GiB file fits in its 16 GiB of RAM.

A single lvmo-iscsi workload already reaches the raw disk's sequential throughput and random write IOPS. Its random reads (one fio job at queue depth 32) stay below the disk's 50 000, which the raw test reached with 4 jobs at queue depth 128. The network and the iSCSI target add about 107 µs to a single read (224 µs against 117 µs on the raw disk).

## Twenty workloads

Computed the same way as the [final matrix](#final-matrix-twenty-parallel-workloads-with-lvmo-iscsi-bounded); the gp3 and AWS columns are copied from there.

| | lvmo-nvme-iscsi | lvmo-nvme-nfs | lvmo-perf-iscsi (gp3, bounded) | lvmo-perf-nfs (gp3) | ebs-gp3 (20 volumes) | efs |
|---|---|---|---|---|---|---|
| Pods completed, fio errors | 20/20, 0 | 20/20, 0 | 20/20, 0 | 20/20, 0 | 20/20, 0 | 20/20, 0 |
| Seq write 1M, total (MiB/s) | **268** | **260** | 128 | 124 | 2 392 | 1 034 |
| Seq read 1M, total (MiB/s) | **339** | **383** ¹ | 130 | 175 | 2 396 | 2 967 |
| Rand write 4k, total (IOPS) | **27 545** | **17 861** | 2 991 | 997 | 59 981 | 32 683 |
| Rand read 4k, total (IOPS) | **50 096** | **50 010** ¹ | 3 015 | 3 462 | 59 989 | 128 604 |
| Seq write per pod, min – max (MiB/s) | 13.3 – 14.1 | 12.9 – 13.2 | 5.9 – 6.8 | 6.2 – 6.3 | 118.9 – 120.8 | 51.6 – 52.3 |
| Rand read 4k QD1, median pod p50 (µs) | **399** | **403** | 6 652 | 5 997 | 545 | 569 |
| Rand read 4k QD1, worst pod p99 (µs) | 545 | 750 | 8 094 | 8 716 | 872 | 6 521 |
| Worst p99, any throughput job (ms) | 1 434 | 1 787 | 7 550 | 4 530 | 184 | 801 |
| What limits the total | The NVMe disk, every job; CPU close behind on random reads | The NVMe disk; on random reads the disk and the server's 2 vCPU together | gp3 disk | gp3 disk | Each volume, and the clients' EBS bandwidth | EFS Elastic throughput |

¹ Partly from the server's page cache: 16 GiB of RAM for 40 GiB of files. During random reads the disk served about 40 000 reads/s for 50 000 client reads/s, so about a fifth came from memory.

Compared with the same class on gp3:

| | Seq write | Seq read | Rand write 4k | Rand read 4k | QD1 median latency |
|---|---|---|---|---|---|
| lvmo-iscsi, NVMe / gp3 | ×2.1 | ×2.6 | ×9.2 | ×16.6 | 17× lower |
| lvmo-nfs, NVMe / gp3 | ×2.1 | ×2.2 | ×17.9 | ×14.4 | 15× lower |

## Reading

- **lvmo-iscsi delivers the disk.** With 20 workloads, every iSCSI total equals the raw disk's limit (268 against 263 MiB/s written, 339 against 334 read, 27 545 against 27 494 random writes, 50 096 against 49 991 random reads), and the disk was 100% busy in every job. lvmo adds no ceiling of its own below what this disk can do. The gains over gp3 follow the disk exactly: ×2 to ×2.6 where the disk is ×2 to ×2.7 faster in throughput, ×9 to ×17 where it is ×9 to ×17 faster in IOPS.
- **Sharing stays fair, and latency drops by an order of magnitude.** Per-pod sequential writes stay within 6% of each other (13.3 to 14.1 MiB/s), as on gp3. With 20 clients queued on one disk, a single 4 KiB read takes 0.4 ms instead of 6.7 ms, below the 545 µs that one EBS volume per pod gave. The worst wait in any job fell from 7.5 s to 1.4 s. The target logged no abort, data timeout or closed connection.
- **lvmo-nfs reaches the disk too, except for random writes.** Sequential writes are at the disk's limit, and reads are slightly above it thanks to the page cache. Random 4 KiB writes reach 17 861 IOPS while the disk performed 27 500 writes/s at 99% utilisation: each synchronous NFS write costs about 1.5 disk writes once ext4's journal and metadata on the server are added. On gp3 the same overhead left NFS at a third of iSCSI's random write IOPS; on NVMe it is two thirds.
- **The server's 2 vCPU are the next limit.** The iSCSI target and the NFS server run in the kernel. During random reads at 50 000 IOPS, the server's CPU was 78% busy for iSCSI (system and softirq time) and fully busy for NFS (0% idle). With a faster disk, or more of them, this server would be CPU-bound before it was disk-bound.
- **The network carried it, on burst credits.** The server sent up to 338 MB/s and received up to 265 MB/s, about 2.7 and 2.1 Gbit/s, three times its 0.78 Gbit/s baseline. The interface's `bw_in_allowance_exceeded` and `bw_out_allowance_exceeded` counters grew (3.7 million and 0.5 million packets queued or dropped), mostly during the prefill and the sequential read job, yet throughput stayed at the disk's limits. This looks like bursts from 20 clients exceeding the momentary allowance rather than a sustained cap. A longer run would use up the burst credits and fall to the baseline of about 93 MiB/s, below even the gp3 disk. A datacenter server has a dedicated NIC instead; on AWS a non-burstable network needs a larger instance.

## What the NVMe run revealed about new iSCSI volumes

Between their first mount and the start of the prefill, the 20 new iSCSI volumes sent the disk 5.35 million writes of exactly 1 KiB (5.8 GB) in about 4.5 minutes. For the first minute this held the disk at its IOPS limit (27 500 writes/s, 99% busy). NFS volumes showed nothing of the kind: their prefill went straight to 262 MB/s in writes of 55 KiB.

The cause, checked on one new volume:

1. `mkfs.ext4` runs on the server, on the new thin LV, but does not mark the inode tables as zeroed (`ITABLE_ZEROED` is unset in all 81 block groups of a 10 GiB volume). After the first mount, the node's `ext4lazyinit` thread zeroes them, about 160 MiB per volume.
2. The node does this with a few large zeroing commands: 49 write commands for 66 MB during the probe. The LUN advertises WRITE SAME (`emulate_tpws=1`).
3. The thin LV under the LUN does not support write-zeroes (`write_zeroes_max_bytes` is 0), so LIO emulates each command with block-sized writes: the 1 KiB writes seen on the disk, about 3 500 per second for a single volume.

The measured amount is about 290 MiB per volume, before the prefill started (small writes kept mixing with the prefill afterwards). The inode tables explain 160 MiB of it; the rest is not explained yet. At gp3's 3000 IOPS, 5.35 million writes take about 30 minutes, which matches the 25 minutes of small writes seen during the gp3 prefill. The ext4 lazy initialization suspected in [the queue-bound section](#bounding-iscsi-queues-so-that-an-overloaded-disk-degrades-gracefully) is confirmed as the main source, together with the way LIO turns it into small writes.

The zeroing is useless: a new thin LV reads as zeros, and the pool zeroes newly provisioned chunks. A candidate fix is to format with `mkfs.ext4 -E assume_storage_prezeroed=1` (e2fsprogs 1.47 and later, the version in Ubuntu 24.04), which marks the inode tables as zeroed without writing them. It is not implemented yet and needs its own test.

## Limits of this run

- One run per class; no variance measured.
- AWS instance store, not a datacenter server: the disk is lost when the instance stops, and the network is burstable.
- The server has twice the RAM of the gp3 runs' server, which helps NFS reads (see note ¹).
- The prefill window was 15 minutes. The gp3 runs used 50 minutes, which their slower prefill needed; the measured jobs are the same.

## Evidence

`.test/reports/perf-nvme-single/` and `.test/reports/perf-nvme/` (fio output per pod, the script's tables, per-pod fairness, the storage server's `iostat`, `mpstat` and network allowance samples, the raw disk's fio output, the kernel log counts), not committed.

# Twenty parallel workloads with five striped gp3 volumes

Run on 2026-10-05 by the striped variant of scenario [performance-nvme-parallel](../tests/scenarios/performance-nvme-parallel.md#variant-five-striped-gp3-volumes), lvmo at commit `8f085ab` (queue bound included).

## Question

The NVMe run answered how many IOPS lvmo can serve, but not how far its sequential throughput goes: that disk stopped at about 340 MiB/s. Here the storage server has five ordinary disks striped together, as an on-premises server with a few SATA or SAS disks would. The prediction was that lvmo would reach the stripe's limits, about 625 MiB/s and 15 000 IOPS. Is that right, and if not, what stops it?

## Environment

As in the gp3 and NVMe runs (EKS 1.35 in eu-west-3, 2 × m6i.2xlarge clients in eu-west-3a, 10 pods on each), with these differences:

| | |
|---|---|
| lvmo storage server | **m6i.4xlarge**, eu-west-3a: 16 vCPU, booted with `mem=16G` to keep the NVMe run's RAM (about 15 GiB usable). Its baselines (EBS 625 MB/s and 20 000 IOPS, network 6.25 Gbit/s) cover the five volumes without burst credits; an m6i.large has 81 MB/s of EBS baseline, below even one gp3 volume |
| lvmo disks | **five gp3 volumes**, 100 GiB, 3000 IOPS and 125 MB/s each, in one VG `lvmo-striped`; thin pool data striped over the five with 64 KiB stripes (`lvcreate --type thin-pool -i 5 -I 64k --chunksize 64k`), 1 GiB of metadata on one of them |
| Classes | `lvmo-striped-iscsi`, `lvmo-striped-nfs` |
| Workload | Unchanged: 20 pods per class, 10 GiB volume and 2 GiB file each, full prefill, then the five jobs together (start spread ≤ 1 s) |

A plain VG without striping would have been pointless: LVM places a volume group's extents one disk after the other, and 40 GiB of test data would have used only the first disk.

## The raw stripe

fio on a plain LV striped the same way, on the server, before the thin pool was created:

| | Seq write 1M | Seq read 1M | Rand write 4k | Rand read 4k | Rand read 4k QD1 p50 / p99 |
|---|---|---|---|---|---|
| 5 × gp3, striped | 625 MiB/s | 625 MiB/s | 14 929 IOPS | 14 983 IOPS | 569 / 741 µs |

Exactly five times one gp3 volume, with gp3's latency.

## One workload

| StorageClass | Seq write 1M (MiB/s) | Seq read 1M (MiB/s) | Rand write 4k (IOPS) | Rand read 4k (IOPS) | Rand read 4k QD1 p50 / p99 (µs) |
|---|---|---|---|---|---|
| lvmo-striped-iscsi | 624.6 | 624.2 | 8 231 | 11 289 | 668 / 864 |
| lvmo-striped-nfs | 622.8 | 1 135.9 ¹ | 2 871 | 72 888 ¹ | 93 / 130 ¹ |

¹ From the server's page cache (4 GiB file, about 15 GiB of RAM).

One lvmo-iscsi workload already reaches the stripe's 625 MiB/s in both directions. Its random IOPS are those of one fio job at queue depth 32 with gp3 latency (about 32 / 3.9 ms ≈ 8 200), not the stripe's limit.

## Twenty workloads

| | lvmo-striped-iscsi | lvmo-striped-nfs, 8 nfsd threads (Ubuntu default) | lvmo-striped-nfs, 64 nfsd threads | lvmo-nvme-iscsi | lvmo-nvme-nfs |
|---|---|---|---|---|---|
| Pods completed, fio errors | 20/20, 0 | 20/20, 0 | 20/20, 0 | 20/20, 0 | 20/20, 0 |
| Seq write 1M, total (MiB/s) | **629** | **619** | **624** | 268 | 260 |
| Seq read 1M, total (MiB/s) | **630** | **672** ² | **736** ² | 339 | 383 ² |
| Rand write 4k, total (IOPS) | **14 990** | 2 895 | **8 626** | 27 545 | 17 861 |
| Rand read 4k, total (IOPS) | **14 976** | 18 179 ² | **19 349** ² | 50 096 | 50 010 ² |
| Seq write per pod, min – max (MiB/s) | 29.8 – 32.8 | 30.8 – 31.5 | 29.2 – 34.1 | 13.3 – 14.1 | 12.9 – 13.2 |
| Rand read 4k QD1, median pod p50 (µs) | 709 | 1 139 | 700 | 399 | 403 |
| Rand read 4k QD1, worst pod p99 (µs) | 5 603 | 1 761 | 5 800 | 545 | 750 |
| Worst p99, any throughput job (ms) | 751 | 558 | 784 | 1 434 | 1 787 |
| What limits the total | The five disks, every job | NFS server threads on random I/O; the disks on sequential I/O | The five disks, every job | The NVMe disk; CPU close behind | The NVMe disk, and CPU on random reads |

² Partly from the server's page cache (about 15 GiB of RAM for 40 GiB of files).

The target logged no abort, data timeout or closed connection.

## Reading

- **The prediction holds for lvmo-iscsi.** Every total is the stripe's limit: 629 and 630 MiB/s against 625, 14 990 and 14 976 IOPS against about 15 000. The disks were 99% busy in the sequential jobs, and their 15 000 operations per second were used in full in the random ones. lvmo-iscsi adds no limit of its own at 630 MiB/s, nearly twice what the NVMe run could test. The server's 16 vCPU stayed almost idle (2% system time; the rest was waiting on the disks), so the CPU limit seen on the 2-vCPU NVMe server came from that server's size, not from lvmo. Twenty workloads share the disks evenly (29.8 to 32.8 MiB/s each).
- **lvmo-nfs is held back by the NFS server's default 8 threads, not by the disks.** With 8 threads, random writes stopped at 2 895 IOPS, with one workload as with twenty, while the disks did 8 800 writes/s, 59% of what they can. Each synchronous NFS write holds a server thread for about three disk writes at gp3's latency (0.9 ms each), and 8 threads at about 2.7 ms per request give about 3 000 requests per second. With 64 threads (`nfsconf --set nfsd threads 64`), random writes rose to 8 626 IOPS, the median single-read latency fell from 1.14 ms to 0.70 ms, and the disks were at their 15 000 operations per second (about 1.7 disk writes per client write) in every job. On NVMe the same 8 threads had been enough, because each disk write took tens of microseconds. **A storage server with disks of ordinary latency needs more NFS server threads than Ubuntu's default.**
- **Sequential throughput is the same for both protocols.** About 625 MiB/s written, and reads at or above the stripe's limit, NFS getting extra from the page cache.
- **Random writes still cost NFS more.** With enough threads, NFS reaches 58% of iSCSI's random write IOPS on the same disks (8 626 against 14 990), the price of synchronous writes and ext4's journal on the server. It was 65% on NVMe.
- **The small writes on new iSCSI volumes, again.** Before the prefill, the 20 new volumes sent 4.5 million writes of 1 KiB, the ext4 lazy initialization described in the [NVMe section](#what-the-nvme-run-revealed-about-new-iscsi-volumes). They took the stripe's full 15 000 operations per second for about 4 minutes before the prefill could start.
- **Network.** The server received up to 1 143 MB/s in short peaks during the prefill, and the interface's allowance counters grew (9.1 million packets in, 0.5 million out), yet throughput stayed at the disks' limits: bursts from 40 client connections, not a cap.

## What this changes

- `scripts/setup-vm.sh` and the README do not set the number of NFS server threads, so a server set up by them gets Ubuntu's 8. Raising it (for example `nfsconf --set nfsd threads 64`) is a proposed change, not made yet.
- lvmo's own path reached 630 MiB/s and 50 000 IOPS (on NVMe) without becoming the limit. Within what was tested, the server's disks, its CPU, and the NFS thread count decide performance, not lvmo.

## Limits of this run

- One run per class; no variance measured.
- The NFS rerun with 64 threads came after the first NFS run on the same server, with new volumes: same workload, same disks.
- The RAM limit (`mem=16G`) keeps the page cache comparable with the NVMe run, not with the gp3 runs (8 GiB).

## Evidence

`.test/reports/perf-striped-single/`, `.test/reports/perf-striped/` and `.test/reports/perf-striped-nfs64/` (fio output per pod, the script's tables, per-pod fairness, the server's `iostat` of the five disks, `mpstat` and network samples, the raw stripe's fio output, the kernel log counts), not committed.
