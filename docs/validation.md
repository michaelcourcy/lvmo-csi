# Validation

Validation performed on 27 September 2026. This is an evaluation implementation, not a production certification.

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
| Release builds | Linux AMD64 and ARM64 | Executables and multiarchitecture OCI image archive built locally |

The external suite excludes disruptive, serial, slow, performance and stress tests. The local run reported 7,626 skipped specs; the OpenShift run reported 6,863. These include unrelated Kubernetes tests and unsupported driver capabilities; they are not passes. External-suite coverage above is for NFS. Both protocols have separate sanity and snapshot coverage.

Local final run: `scripts/e2e.sh local sanity snapshots metadata`. The external suite was run separately during development. Azure sanity, snapshot, metadata, and external acceptance were validated across separate runs while fixing the harness. The final external rerun used `scripts/e2e.sh azure external`. Its 38 tests and physical cleanup audit passed; a missing final success marker caused the wrapper to exit nonzero, and the marker propagation was then corrected and checked separately. Logs and report archives are under `.test/reports/` (not committed).

Kasten export/restore, `preferred` fallback, and Kasten consumption of KEP-3314 remain unvalidated. Passing the independent metadata client does not establish Kasten interoperability. Production load, thin-pool exhaustion recovery, server failure/fencing, and long-running durability testing remain outside this initial validation.

Release files are in `dist/`; building them does not publish a GitHub release or Docker Hub tag.
