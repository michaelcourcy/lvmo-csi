---
id: cbt-reconstruction-driver
status: automated
groups: [backup, cbt]
requires: [test-storage-server]
automation: scripts/run-scenarios.sh cbt-reconstruction-driver
---

# Full and incremental backups built from the driver's snapshot metadata reproduce the snapshots byte for byte

## Purpose

This is the core of KEP-3314 support. A backup client takes the allocated ranges of a first snapshot (full backup), then the changed ranges between two snapshots (incremental backup), reads those ranges from a raw block clone of each snapshot, and rebuilds the images. The rebuilt images must equal the snapshots exactly, including filesystem metadata and ranges that were overwritten, discarded or zeroed, so that no stale data survives a restore. This scenario calls the driver's CSI services directly; [cbt-metadata-endpoint](cbt-metadata-endpoint.md) checks the same workflow through Kubernetes and the public endpoint.

## Preconditions

- The storage server runs `lvmo-driver` on `unix:///tmp/lvmo-csi.sock` (`scripts/setup-vm.sh`).
- The test binary `bin/integration.test` for the server's architecture: `go test -c -o bin/integration.test ./tests/integration`.

## Steps

1. On the storage server: `scripts/run-scenarios.sh cbt-reconstruction-driver`. It runs `TestBackupReconstruction` for three source modes: NFS, iSCSI filesystem, and iSCSI raw block.

## Expected

- The test passes for all three modes: every reconstructed image matches its snapshot clone byte for byte.

## Evidence

- Test output for each mode.

## Cleanup

- Done by the test. The cleanup audit must pass afterwards.
