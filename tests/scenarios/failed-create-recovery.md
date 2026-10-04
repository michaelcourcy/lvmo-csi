---
id: failed-create-recovery
status: automated
groups: [resilience]
requires: [test-storage-server]
automation: scripts/run-scenarios.sh failed-create-recovery
---

# A volume whose creation failed is reclaimed, and the same name can be retried

## Purpose

If creating a volume fails halfway, for example because `mkfs` fails after the LV exists, the leftovers must be reclaimed. Otherwise the PVC name stays blocked forever and space leaks. A retry with corrected parameters and the same name must then succeed.

## Preconditions

- The storage server runs `lvmo-driver` on `unix:///tmp/lvmo-csi.sock`, with VG `lvmo-test2` (`scripts/setup-vm.sh`).
- The test binary `bin/integration.test` for the server's architecture.

## Steps

1. On the storage server: `scripts/run-scenarios.sh failed-create-recovery`. It runs `TestFailedCreateRecovery`, which requests an iSCSI XFS volume too small for XFS, expects the creation to fail, then retries the same name with a valid request.

## Expected

- The first creation fails; the retry succeeds and returns a usable volume.
- The test deletes the volume, and no LV remains for it.

## Evidence

- Test output.

## Cleanup

- Done by the test.
