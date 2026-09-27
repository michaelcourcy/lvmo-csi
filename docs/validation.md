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
| OpenShift deployment | Current Azure cluster, 12 nodes | Controller and all node plugins became ready |
| Ordinary snapshot restores | OpenShift, NFS and iSCSI | Same-namespace and cross-namespace restores passed |
| Release builds | Linux AMD64 and ARM64 | Executables and multiarchitecture OCI image archive built locally |

The external suite excludes disruptive, serial, slow, performance and stress tests. Its reported 7,626 skipped specs include unrelated Kubernetes tests and unsupported driver capabilities; they are not passes. External-suite coverage above is for NFS. Both protocols have separate sanity and snapshot coverage.

Local final run: `scripts/e2e.sh local sanity snapshots metadata`. The external suite was run separately during development. Logs and report archives are under `.test/reports/` (not committed).

Kasten export/restore, `preferred` fallback, and Kasten consumption of KEP-3314 remain unvalidated. Passing the independent metadata client does not establish Kasten interoperability. Production load, thin-pool exhaustion recovery, server failure/fencing, and long-running durability testing remain outside this initial validation.

Release files are in `dist/`; building them does not publish a GitHub release or Docker Hub tag.
