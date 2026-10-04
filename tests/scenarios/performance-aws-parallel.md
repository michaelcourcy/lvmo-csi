---
id: performance-aws-parallel
status: manual
groups: [performance]
requires: [kubernetes, storage-server, nfs-client, iscsi-client, aws-storage]
automation: scripts/test-performance.sh
---

# Twenty parallel workloads: how lvmo scales compared with EBS and EFS

## Purpose

[performance-aws-baseline](performance-aws-baseline.md) measured one workload at a time. This scenario runs 20 at once, each on its own volume, to see how total performance scales. The expectation is that AWS storage scales better: every EBS volume brings its own provisioned performance, and EFS is a distributed service, while lvmo serves every volume from one storage server and its disk. The results show where each side's limit lies, and are added to [docs/performances-test.md](../../docs/performances-test.md).

## Preconditions

- The storage setup of `performance-aws-baseline`: VG `lvmo-perf` on a dedicated gp3 disk (3000 IOPS, 125 MB/s), StorageClasses `lvmo-perf-iscsi`, `lvmo-perf-nfs`, `ebs-gp3` and `efs`.
- Client nodes that do not limit the result: two non-burstable instances (m6i.2xlarge) in the storage server's availability zone, labelled `lvmo-bench=true` and tainted so that nothing else runs there.

## Steps

1. Record the EBS and network limits of the client and storage server instance types (`aws ec2 describe-instance-types`).
2. Run `REPLICAS=20 FILE_SIZE=2g NODE_LABEL=lvmo-bench=true scripts/test-performance.sh lvmo-perf-iscsi ebs-gp3 lvmo-perf-nfs efs`. For each class, it creates 20 PVCs and 20 fio pods spread evenly across the client nodes. All 20 pods start the same five jobs as in the baseline at the same moment, on a 2 GiB file each.
3. Add a section to `docs/performances-test.md` with the environment, the totals table, a comparison with the single-workload baseline, and the limit each class reached.

## Expected

- fio completes in all 80 pods, and the start spread of each class is a few seconds at most.
- The report explains each total by a known limit (disk, instance network or EBS bandwidth, service limit), or says that none was found.
- The expectation that EBS and EFS scale better than lvmo is confirmed or refuted by the numbers, not assumed.

There is no pass threshold: this scenario measures, it does not judge.

## Observations

- Prefill is required: without it, reads of never-written blocks return zeros without disk access, on thin LVs and new EBS volumes alike.
- The prefill window must cover the slowest backend's prefill; on lvmo-iscsi with this disk it did not, which is itself the result.

## Validation

Run on `eks-paris` on 2026-10-04; results in [docs/performances-test.md](../../docs/performances-test.md#twenty-parallel-workloads-on-aws-storage-class). EBS and EFS scaled; lvmo-nfs stayed at its disk's limit without errors; lvmo-iscsi collapsed (12/20 pods completed).

## Evidence

- `.test/reports/perf/<class>/<n>.json` for each class and pod, the instance limits, and the script's table.

## Cleanup

- The script deletes its namespace and PVCs. Remove the client node group if it was created only for this run.
