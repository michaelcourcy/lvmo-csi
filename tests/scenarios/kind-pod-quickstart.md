---
id: kind-pod-quickstart
status: manual
groups: [basic]
requires: [kubernetes, nfs-client, iscsi-client]
automation: none
---

# Kind quickstart with a PVC-backed storage-server Pod

## Purpose

Validate [the Kind quickstart](../../docs/quickstart-kind.md) on a dedicated Linux
host/VM without installing a standalone lvmo storage server on that host.

## Preconditions

Use an explicitly named `kind-linux` environment instance with the required
host modules and rootful Docker. Record its VM retention policy. No unrelated
NFS/LIO server or existing `lvmo-pod` Kind cluster may be present.

## Steps

1. Follow the quickstart's host preparation and run `scripts/quickstart-kind.sh`.
   Record custom node image, driver/server image IDs and component versions.
   Allow 20 minutes for builds/downloads and 5 minutes for chart readiness.
2. Verify the source PVC binds on `standard`, the storage server is in
   `lvmo-system`, and both generated classes point to that server. The backing
   file must be smaller than the requested 5Gi even though local-path exposes
   the host filesystem's larger capacity.
3. Apply `examples/kind/workloads.yaml`. Both Pods must be Ready within 3 minutes.
   Write a distinct file through each protocol and record SHA-256 hashes and
   actual mount types. For iSCSI, verify the host device timeout is 120 seconds;
   this exercises the nested helper through `/proc/.../root`. Stop the consumer Pods, keep their PVCs, and recreate
   the Pods; hashes must match within 3 minutes.
4. Try uninstall while volumes exist: the guard must refuse removal and both
   volumes must remain readable. Delete consumer resources and wait up to 10
   minutes for backend reclamation, then retry uninstall successfully.
5. Verify the backing PVC is retained. Explicitly delete it, verify no owned
   loop devices/VGs/targets remain, then delete Kind. Follow the instance's VM
   retention policy and write the report.

## Expected

- Cluster creation and Helm installation work using the documented commands.
- Both protocols support file writes and preserve hashes after consumer restart.
- The server uses a bounded backing file, not most of the local host disk.
- Nonempty uninstall is refused; empty uninstall retains the source PVC.
- Explicit cleanup removes owned storage and Kind; retained VM policy is honored.

## Evidence

Record build/install logs, image IDs, rendered values without credentials,
source PV and protocol mount evidence, hashes, guard logs and cleanup inventory.
Report under `.test/reports/<date>-<instance>-kind-pod-quickstart.md`.

## Cleanup

Remove consumers and snapshots while CSI and the server remain running. Wait for
reclamation, uninstall, then delete the retained backing PVC. Verify kernel
resources are reclaimed before removing Kind. Keep the Linux VM if requested.

## Design notes

All Kind nodes share one kernel. This does not validate distinct initiators,
node-loss fencing, production durability or independent worker failures.

## Observations

Passed on 8 October 2026 in the retained `kind-pod` Linux VM, using a fresh Kind
cluster and the documented script. Both protocols preserved distinct hashes
after consumer restart and refused uninstall. Empty uninstall retained the
backing PVC; explicit deletion and Kind cleanup left no owned kernel resources.
Report: `.test/reports/2026-10-08-kind-pod-kind-pod-quickstart.md` (not committed).
