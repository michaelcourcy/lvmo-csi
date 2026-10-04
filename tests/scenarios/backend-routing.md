---
id: backend-routing
status: automated
groups: [routing]
requires: [kubernetes, test-storage-server, cluster-on-storage-server, nfs-client, iscsi-client]
automation: scripts/run-scenarios.sh backend-routing
---

# Each StorageClass reaches its own storage server, and backends stay isolated

## Purpose

One driver installation can serve several storage servers: each StorageClass names its API endpoint, and each volume ID records which server owns it. Every operation on a volume or snapshot must reach that server, and never another one, including after the driver restarts.

## Preconditions

- The driver is installed and the storage server is a test server (`scripts/setup-vm.sh`). The script starts a second, independent API on port 50052 on the same host, with its own state directory and its own loop-backed VG.

## Steps

1. On the storage server: `scripts/run-scenarios.sh backend-routing`. It enables the metadata sidecar if needed, then runs `scripts/test-routing.sh`.

## Expected

- Volumes and snapshots are created on the server their StorageClass names.
- Capacity and snapshot metadata are reported by the right server.
- After the driver's controller and node pods restart, existing volumes are still found on their servers.
- Cloning across servers is refused.
- An NFS-to-iSCSI backup clone works on the second server.
- At the end, the second API's volumes, VG and state are removed.

## Evidence

- The script's output, ending with `Per-StorageClass routing, cold discovery, second-backend CBT, and cleanup passed.`

## Cleanup

- The script stops the second API and removes its loop device, VG and state directory on exit.
