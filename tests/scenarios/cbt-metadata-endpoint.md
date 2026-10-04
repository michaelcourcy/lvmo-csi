---
id: cbt-metadata-endpoint
status: automated
groups: [backup, cbt]
requires: [kubernetes, storage-server, nfs-client, iscsi-client]
automation: scripts/run-scenarios.sh cbt-metadata-endpoint
---

# A backup client using the public KEP-3314 endpoint rebuilds snapshots exactly, and only authorized clients get access

## Purpose

Backup applications reach changed block tracking through the Kubernetes snapshot-metadata service: discovery through the `SnapshotMetadataService` object, TLS, and the sidecar's authentication and authorization. This scenario runs the full and incremental reconstruction workflow of [cbt-reconstruction-driver](cbt-reconstruction-driver.md) the way a real backup client would, from a pod, using the `lvmo-metadata` client and raw block clones of the snapshots.

## Preconditions

- The driver is installed with StorageClasses `lvmo-nfs` and `lvmo-iscsi`.
- The snapshot-metadata sidecar is enabled. The automation enables it with `scripts/enable-metadata.sh` if it is not yet.

## Steps

1. From a machine with the cluster's kube context: `scripts/run-scenarios.sh cbt-metadata-endpoint`. It runs `scripts/test-metadata.sh` for each source mode: `nfs`, `iscsi-filesystem`, `iscsi-block`. Besides reconstruction, each run checks stream continuation, interrupted streams, concurrent queries and clone reads, invalid and reversed snapshot requests, and that unauthorized callers are refused.

## Expected

- All three modes complete; every reconstructed image matches its snapshot clone byte for byte.
- Unauthorized requests are rejected.

## Evidence

- Script output for each mode, including the comparison results and the authorization checks.

## Cleanup

- The script deletes its namespace, cluster role and binding. The metadata sidecar stays enabled.
