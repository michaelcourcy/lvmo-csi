---
id: csi-sanity
status: automated
groups: [basic]
requires: [test-storage-server]
automation: scripts/run-scenarios.sh csi-sanity
---

# The driver passes the upstream CSI conformance suite on both protocols

## Purpose

[csi-sanity](https://github.com/kubernetes-csi/csi-test) is the upstream conformance suite for CSI drivers. It calls the driver's gRPC services directly, without Kubernetes, and checks that each implemented call behaves as the CSI specification requires: arguments validated, correct error codes, idempotent retries, and consistent volume, snapshot and expansion life cycles. Passing it means any CSI orchestrator can rely on the driver's basic contract. The suite itself defines the individual checks; this scenario does not repeat them.

## Preconditions

- The storage server runs `lvmo-driver` on `unix:///tmp/lvmo-csi.sock`, with VG `lvmo-test1`, as set up by `scripts/setup-vm.sh`.
- The test binary `bin/sanity.test`, built for the server's architecture: `go test -c -o bin/sanity.test ./tests/sanity`.

## Steps

1. On the storage server, from the source directory: `scripts/run-scenarios.sh csi-sanity`. It runs the suite once with `PROTOCOL=nfs` and once with `PROTOCOL=iscsi`, then the cleanup audit.

## Expected

- Both runs report zero failed specs.
- The cleanup audit passes (see [cleanup-audit](cleanup-audit.md)).
- Skipped and pending specs are reported as such, never counted as passes. Reference: 75 passed, 1 upstream pending, 16 skipped per protocol since the driver advertises `PUBLISH_UNPUBLISH_VOLUME` (67 passed and 24 skipped before, see [docs/validation.md](../../docs/validation.md)). A drop in passed specs must be explained.

## Evidence

- `.test/reports/sanity-nfs.log` and `.test/reports/sanity-iscsi.log`, and the passed, failed, pending and skipped counts of each.

## Cleanup

- None beyond what the suite and the cleanup audit already do.
