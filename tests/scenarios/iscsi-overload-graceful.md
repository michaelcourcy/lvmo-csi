---
id: iscsi-overload-graceful
status: manual
groups: [resilience, performance]
requires: [kubernetes, storage-server, iscsi-client, multi-node]
automation: none
---

# Twenty iSCSI workloads overloading one disk slow down evenly instead of failing

## Purpose

When the iSCSI volumes on a storage server ask for more than its disk can do, every workload must slow down, fairly, and keep running. Without a bound, each session queued dozens of commands; on an overloaded disk they waited longer than the clients' 30-second SCSI timeout, were aborted and resent, sessions were closed, and pods hung or failed (see [performance-aws-parallel](performance-aws-parallel.md)). lvmo bounds the commands in flight per volume on the target and on the node (`--iscsi-queue-depth`, default 8) and gives nodes a longer command timeout (`--iscsi-command-timeout`, default 120 s).

## Preconditions

- An iSCSI StorageClass on a VG whose disk can be saturated by 20 workloads (for example one gp3 volume of 3000 IOPS and 125 MB/s). The node's own iSCSI settings are the distribution's defaults: lvmo applies the bound itself.
- Client nodes that do not limit the result (see `performance-aws-parallel`), labelled for `NODE_LABEL`.

## Steps

1. Create a test volume and check that the bound is applied: on the storage server, the target's `default_cmdsn_depth` and its ACL's `cmdsn_depth` are the configured depth; on the node, the disk's `/sys/block/sdX/device/queue_depth` is the depth and `device/timeout` the configured timeout. Delete the volume.
2. Record the time, then run `REPLICAS=20 FILE_SIZE=2g PREFILL_WINDOW=3000 NODE_LABEL=<label> scripts/test-performance.sh <iscsi class>`, sampling the data disk with `iostat -dxmt <disk> 10` on the storage server.
3. After the run, count `ABORT_TASK`, `DataOut timeout` and `closing iSCSI connection` messages in the storage server's kernel log since the recorded time.

## Expected

- Step 1: the target and the node show the configured depth and timeout.
- All 20 pods complete, and no fio job reports an error.
- No `ABORT_TASK`, `DataOut timeout` or closed connection on the target.
- The totals are close to the disk's limits, and each pod's sequential write throughput is within a factor of 2 of the median.
- The worst pod's p99 latency stays below the command timeout.

## Evidence

- The bound as seen on the target and the node, the script's table, per-pod throughput and latency, the kernel log counts, and the disk samples.

## Cleanup

- The script deletes its namespace and PVCs. Stop the disk sampler.

## Validation

Passed on `eks-paris` on 2026-10-05 with image `dev-iscsi-queue` (default depth 8, timeout 120 s): the bound was applied on the target and the node by lvmo alone; 20/20 pods completed; no abort, data timeout or closed connection; totals at the disk's limits; per-pod sequential writes 5.9–6.8 MiB/s; worst p99 4.9 s. Results in [docs/performances-test.md](../../docs/performances-test.md#bounding-iscsi-queues-so-that-an-overloaded-disk-degrades-gracefully).

## Design notes

- The bound is a property of the storage server's disk, the same for every volume sharing it: it belongs to the API, not to StorageClasses, where different values would turn it into an accidental priority between classes.
- The target sets the window on the portal group before any ACL exists, because ACLs inherit it on creation and `targetcli` cannot change an existing ACL's window. A changed value applies to a volume the next time it is published.
- The node derives `cmds_max` from the depth: Linux keeps 15 session slots for task management and needs a power of two of at least 16, so 16 would leave one usable command.
- The bound does not fix the disk's throughput under small writes; it keeps the overload from turning into failures.
