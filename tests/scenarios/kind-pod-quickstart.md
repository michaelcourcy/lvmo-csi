---
id: kind-pod-quickstart
status: manual
groups: [basic]
requires: [kubernetes, nfs-client, iscsi-client]
automation: none
---

# Kind quickstart with a PVC-backed storage-server Pod

> Two-chart revision; runtime validation of the updated quickstart is pending.
> Historical observations apply to the previous combined installation.

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
   Allow 20 minutes for builds/downloads and 5 minutes per release for readiness.
   Verify releases `lvmo` (driver) and `server-a` (server) in `lvmo-system`,
   separate saved values files, and driver-owned `lvmo-snapshots`.
2. Verify the source PVC binds on `standard`, the storage server is in
   `lvmo-system`, and both generated classes point to that server. The backing
   file must be smaller than the requested 5Gi even though local-path exposes
   the host filesystem's larger capacity. The rendered values must not require
   `node-name`, `server-address` or `nfs-clients`. Check `hostNetwork: false`,
   the `server-a-storage` ClusterIP Service, and its EndpointSlice selecting the
   server Pod IP. On the Linux host, `ip route get <Service-IP>` must route
   through the Kind node; save this address for cleanup.
3. Apply `examples/kind/workloads.yaml`. Both Pods must be Ready within 3 minutes.
   Write a distinct file through each protocol and record SHA-256 hashes and
   actual mount types. NFS sources and host iSCSI sessions must use the Service
   IP, not the Pod or node IP. For iSCSI, verify the host device timeout is 120 seconds;
   this exercises the nested helper through `/proc/.../root`. Stop the consumer Pods, keep their PVCs, and recreate
   the Pods; hashes must match within 3 minutes.
4. Try `helm uninstall server-a -n lvmo-system --wait --timeout 2m` while volumes exist: the guard must refuse removal and both
   volumes must remain readable. Delete consumer resources and wait up to 10
   minutes for backend reclamation, then retry server uninstall successfully. The driver and shared snapshot
   class must still exist.
5. Verify the backing PVC is retained. Explicitly delete it, verify no owned
   loop devices/VGs/targets remain, remove the saved Service-IP host route as
   documented, then uninstall driver release `lvmo` and delete Kind. Follow the instance's VM
   retention policy and write the report.

## Expected

- Cluster creation and two independent Helm installations work using the
  documented commands. Only the driver owns the shared snapshot class.
- The server uses Pod networking behind a stable Service, with no explicit
  node/IP values. The host iSCSI route reaches that Service.
- Both protocols support file writes and preserve hashes after consumer restart.
- The server uses a bounded backing file, not most of the local host disk.
- Nonempty uninstall is refused; empty uninstall retains the source PVC.
- Explicit cleanup removes owned storage, the Service-IP host route and Kind; retained VM policy is honored.

## Evidence

Record build/install logs, image IDs, rendered values without credentials,
source PV, Service/EndpointSlice addresses, host route, protocol mount/session
evidence, hashes, guard logs and cleanup inventory.
Report under `.test/reports/<date>-<instance>-kind-pod-quickstart.md`.

## Cleanup

Remove consumers and snapshots while CSI and the server remain running. Wait for
reclamation, uninstall the server, then delete the retained backing PVC.
Uninstall the driver last, after all snapshot cleanup. Verify kernel
resources are reclaimed and remove the saved Service-IP host route before
removing Kind. Keep the Linux VM if requested.

## Design notes

All Kind nodes share one kernel. This does not validate distinct initiators,
node-loss fencing, production durability or independent worker failures.

## Observations

The previous host-network implementation passed on 8 October 2026 in the retained `kind-pod` Linux VM, using a fresh Kind
cluster and the documented script. Both protocols preserved distinct hashes
after consumer restart and refused uninstall. Empty uninstall retained the
backing PVC; explicit deletion and Kind cleanup left no owned kernel resources.
Report: `.test/reports/2026-10-08-kind-pod-kind-pod-quickstart.md` (not committed).

The Pod-network/Service revision and its host route still need runtime validation.
