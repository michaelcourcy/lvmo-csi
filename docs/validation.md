# Validation

Baseline validation performed on 27 September 2026; Kasten block-export results added from 6–7 October 2026 and Pod-network storage-server results from 9 October 2026. Independent Helm charts and server lifecycle were validated on a recreated EKS cluster on 10 October 2026. This is an evaluation implementation, not a production certification.

| Check | Environment | Result |
| --- | --- | --- |
| Go race tests, vet, Helm lint | macOS ARM64 | Passed; Linux integration packages skip without a live endpoint |
| CSI sanity, NFS | Ubuntu 24.04 ARM64 / Lima | 67 passed, 1 upstream pending, 24 skipped |
| CSI sanity, iSCSI | Ubuntu 24.04 ARM64 / Lima | 67 passed, 1 upstream pending, 24 skipped |
| Upstream external storage, NFS | Kind / Kubernetes 1.35.0 | 40 applicable tests passed |
| Ordinary snapshot restores | Kind, NFS and iSCSI | Same-namespace and cross-namespace restores passed |
| CSI full/incremental reconstruction | Lima, all three source modes | Byte comparisons passed |
| Public KEP-3314 endpoint | Kind, all three source modes | Reconstruction, continuation, interrupted-stream recovery, concurrent reads, invalid/reversed snapshots, authentication and authorization passed |
| Native block discard | Kind / iSCSI | Incremental reconstruction correctly replaced previously allocated bytes with zeros |
| Failed-create recovery | Lima | Failed XFS creation reclaimed; retry with the same name succeeded |
| Physical cleanup | Fresh local run | No managed volumes, snapshots, leases, tagged LVs, project iSCSI sessions or targets remained |
| OpenShift deployment | OpenShift 4.18.6 / Kubernetes 1.31.6, 12 nodes | Controller and all node plugins became ready |
| Upstream external storage, NFS | OpenShift / Kubernetes 1.31.6 | 38 applicable tests passed, no failures |
| Ordinary snapshot restores | OpenShift, NFS and iSCSI | Same-namespace and cross-namespace restores passed |
| Public KEP-3314 endpoint | OpenShift, all three source modes | Full/incremental reconstruction, discard, continuation, interruption, concurrency, and access controls passed |
| CSI reconstruction and failed-create recovery | Azure Ubuntu 24.04 AMD64 | Passed |
| Physical cleanup | Azure after upstream external suite | No managed volumes, snapshots, node leases, tagged LVs, or project iSCSI resources remained |
| Per-StorageClass backend routing | Kind; two API processes and independent LVM pools on one Lima VM | Driver restart discovery, capacity, snapshot metadata, cross-backend clone rejection, second-backend NFS-to-iSCSI backup, isolation and physical cleanup passed |
| PVC-backed Pod-network storage server | EKS 1.35.8 / Ubuntu 24.04 AMD64, EBS gp3 | Cross-node NFS/iSCSI, snapshot restores, changed Pod IPs behind a stable Service, mounted-client recovery, lifecycle guards and cleanup passed |
| Independent driver/server Helm charts | Recreated EKS 1.35 / Ubuntu 24.04 AMD64, EBS gp3 | Three servers across two namespaces, six NFS/iSCSI snapshot restores through one shared class, independent upgrades/removal and collision rejection passed |
| Separate server-chart lifecycle | Same recreated EKS cluster | Graceful replacements, mounted-client recovery, immutable-setting rejection, nonempty/uninspectable uninstall guards, retained backing PVC and cleanup passed |
| Release builds | Linux AMD64 and ARM64 | Executables and multiarchitecture OCI image archive built locally |

The external suite excludes disruptive, serial, slow, performance and stress tests. The local run reported 7,626 skipped specs; the OpenShift run reported 6,863. These include unrelated Kubernetes tests and unsupported driver capabilities; they are not passes. External-suite coverage above is for NFS. Both protocols have separate sanity and snapshot coverage.

The baseline local full run (`scripts/e2e.sh local`) passed all suites and cleanup. After adding per-StorageClass routing, `scripts/e2e.sh local metadata sanity snapshots external` passed all functional suites in a fresh Lima/Kind environment: both sanity suites (67 each), both snapshot suites, all metadata modes, the two-backend acceptance test, and 40 upstream NFS tests. The final cleanup audit timed out: one deleted NFS volume remained mounted because the server retained a delegation for a client in courtesy state. Its durable deletion tombstone remained present; it was no longer exported. Consequently, the overall harness exited nonzero. The two-backend test's own physical cleanup had passed. This delayed NFS reclamation needs further work; the disposable VM was removed after preserving the reports. Azure sanity, snapshot, metadata, and external acceptance were validated across separate runs while fixing the harness. The final external rerun used `scripts/e2e.sh azure external`. Its 38 tests and physical cleanup audit passed; a missing final success marker caused the wrapper to exit nonzero, and the marker propagation was then corrected and checked separately. Logs and report archives are under `.test/reports/` (not committed).

The [Kasten block-mode scenario](../tests/scenarios/kasten-block-mode-export.md) ran on EKS (`eks-paris`) on 6–7 October 2026 with Kasten 9.0.7 and lvmo commit `c6bf41d`:

| Check | Result |
| --- | --- |
| iSCSI Filesystem PVC, full block export and object-storage restore | Passed: 340,000,000 PostgreSQL accounts restored |
| Same PVC after deleting 20%, block export and object-storage restore | Passed: 272,000,000 accounts restored, zero in the deleted range |
| NFS Filesystem PVC, full block export through annotated iSCSI class | Passed: temporary clone and data mover used iSCSI Block access |
| Direct restore of that block export to NFS | Unsupported in this path: raw Block attachment rejected with `NFS requires filesystem access`; restore cancelled |
| NFS after-deletion export/restore | Not run after the full restore limitation was established |
| Storage cleanup audit | Passed; no managed volumes, snapshots, leases or tagged LVs remained |

This was not a full pass of the original NFS round-trip scenario. The NFS-source export was not restored onto an iSCSI application PVC, and manual LV rebinding to NFS was not tested. Run evidence is in `.test/reports/2026-10-06-eks-paris-kasten-block-mode-export.md` (not committed). Use ordinary filesystem export when the recovered application must use NFS.

The [Pod storage-server scenario](../tests/scenarios/pod-storage-server.md) passed all nine steps on `eks-paris` on 9 October 2026, using commit `eb862f4` plus the Pod-network/Service changes. A startup-order bug was fixed by starting D-Bus before targetcli, followed by a clean rerun. Both protocols preserved 16MiB file hashes through snapshots and graceful server replacement. The Service IP stayed fixed while Pod IPs changed; existing mounted consumers recovered and accepted new writes within 3 seconds for iSCSI and 92 seconds for NFS after API readiness. Unchanged upgrades, nonempty/unreachable removal guards, and empty disable/re-enable with the same VG UUID passed. Cleanup removed test volumes, kernel storage objects and temporary cloud resources while retaining the original cluster and workers. Evidence: `.test/reports/2026-10-09-eks-paris-pod-storage-server.md` (not committed).

This EKS result does not validate the revised Kind host-route path, NFS lock/delegation reclaim, or abrupt node loss. NFS client recovery tracking was unavailable in the tested container; the observed recovery covers file I/O after graceful replacement.

The [independent Helm charts](../tests/scenarios/independent-helm-charts.md) and
[Pod storage-server](../tests/scenarios/pod-storage-server.md) scenarios passed
on 10 October 2026 using commit `c2e9d70` plus the chart-split changes. The old
EKS cluster and VPC were deleted and recreated before testing. One server ran
in the driver namespace and two in a separate namespace; two servers shared a
worker. All six snapshot restores matched their source hashes through one
shared VolumeSnapshotClass. Independent upgrades and server removal preserved
the other releases; a colliding StorageClass prefix was rejected.

The lifecycle run preserved the VG UUID and Service IP through changed Pod
IPs. Mounted clients recovered and verified fresh writes in 1.4 seconds for
iSCSI and 89 seconds for NFS. Both nonempty and unschedulable-inspection
uninstall guards refused removal; empty uninstall retained the backing PVC
for explicit deletion. Cleanup removed all test resources and temporary ECR,
with host loop inventories matching baseline and no owned device-mapper or
iSCSI resources remaining. The rebuilt cluster and three Ready workers were
retained. Local Go race tests, vet, both-chart rendering/packaging checks and
release binary builds also passed. Kind runtime and public release publication
were not part of this EKS run. Reports (not committed):
`.test/reports/2026-10-10-eks-paris-independent-helm-charts.md` and
`.test/reports/2026-10-10-eks-paris-pod-storage-server.md`.

The [block-mode Pod storage-server](../tests/scenarios/pod-storage-server-block.md)
scenario passed all eight steps on `eks-paris` on 10 October 2026, using commit
`7fddf0d` plus the block-mode changes, with EBS gp3 backing. The first run
found that containerd does not create a `volumeDevices` node in a container
that also mounts the host's `/dev`. An init container now records the device
number and the server recreates the node; the scenario was then rerun from the
start. An `Immediate` source class was refused before any resource was
created. The VG's only physical volume was the Block PVC, with no loop device
or backing file and auto-activation disabled; the Block and state PVCs were
placed in the server's zone. NFS and iSCSI hashes held through snapshot
restores and a graceful replacement that kept the VG UUID. Changing
`block-mode` or `state-size` on upgrade was refused, and the uninstall guard
inspected the state PVC. Cleanup matched the host baseline. Report (not
committed): `.test/reports/2026-10-10-eks-paris-pod-storage-server-block.md`.

Kasten `preferred` fallback and consumption of KEP-3314 remain unvalidated. Passing the independent metadata client does not establish Kasten interoperability. Production load, thin-pool exhaustion recovery, abrupt server failure/fencing, and long-running durability testing remain outside this initial validation.

Release files are in `dist/`; building them does not publish a GitHub release or Docker Hub tag.
