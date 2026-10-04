---
id: node-drain
status: manual
groups: [basic, resilience]
requires: [kubernetes, storage-server, nfs-client, iscsi-client, multi-node, distinct-initiators]
automation: none
---

# Draining a node moves its lvmo volumes cleanly to other nodes

## Purpose

Draining is the planned way to empty a node, for maintenance or an upgrade. Pods are evicted gracefully, so each volume must be unmounted, logged out and detached by the normal path, then attached elsewhere, quickly and without any of the failure machinery: no fencing, no failover action, no filesystem journal recovery. Draining must also not be blocked by lvmo's own pods.

## Preconditions

- StorageClasses `lvmo-nfs` and `lvmo-iscsi`.
- At least two schedulable workers besides the one drained.

## Steps

1. Create namespace `lvmo-drain` with two single-replica Deployments on the same node, node A, each appending a timestamp to `/data/log` every second: one on a 1Gi RWO `Filesystem` PVC on `lvmo-iscsi`, one on a 1Gi RWX PVC on `lvmo-nfs`. Wait for both to be Ready.
2. Record the last line of each `/data/log`, and the iSCSI volume's ACL and ownership on the storage server.
3. Run `kubectl drain <node A> --ignore-daemonsets --delete-emptydir-data --timeout=5m`. Record its duration.
4. Wait up to 3 minutes for both pods to be Ready on other nodes.
5. Read both logs. On the new iSCSI node, check the kernel log for the filesystem mount.
6. Record the iSCSI volume's ACL, ownership and sessions on the storage server, the iSCSI sessions on node A, and the controller's failover log.
7. Run `kubectl uncordon <node A>`.

## Expected

- The drain completes without error and without needing `--force` or `--disable-eviction`.
- Both pods are Ready on other nodes within 3 minutes of the drain finishing.
- Both logs contain the line recorded in step 2.
- The iSCSI filesystem mounts on the new node without a journal recovery.
- The storage server admits only the new node's initiator, and only the new node owns the volume; node A has no session left to the target.
- The controller logs no failover action.

## Validation

Passed on `eks-paris` on 2026-10-03: the drain took 32 s, including evicting the lvmo controller, and both pods were Ready elsewhere 8 s later with their data, the iSCSI filesystem mounted without recovery.

## Evidence

- Drain output and duration; pod placement before and after with timestamps.
- Log checks, kernel log line for the mount.
- Storage server state before and after; node A's `iscsiadm -m session`; controller log.

## Cleanup

- Uncordon node A. Delete namespace `lvmo-drain`. Check on the storage server that nothing remains.
