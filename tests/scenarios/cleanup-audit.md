---
id: cleanup-audit
status: automated
groups: []
requires: [test-storage-server]
automation: scripts/run-scenarios.sh cleanup-audit
---

# After the tests, nothing they created is left on the storage server

## Purpose

Deleting a PVC or snapshot in Kubernetes is not enough: the LV, the export or iSCSI target, the sessions and the API's records must all go too. This audit checks the storage server directly after other scenarios have run. Run it last, on a storage server dedicated to testing: it expects the server to manage nothing at all.

## Steps

1. On the storage server: `scripts/run-scenarios.sh cleanup-audit`. It runs `scripts/check-cleanup.sh`, which waits up to 2 minutes for deletions to complete.

`run-scenarios.sh` adds this scenario at the end of every run, which is why it belongs to no group.

## Expected

- The API state `/var/lib/lvmo/state.json` lists no volumes, snapshots or owners.
- No LV tagged `lvmo` remains in `lvmo-test1` or `lvmo-test2`.
- No iSCSI session or target with the `iqn.2026-09.io.lvmo:` prefix remains.

## Evidence

- The script's output. On failure, it prints the counts still present in the state file.

## Observations

- A deleted NFS volume can stay mounted on the server while an NFS client still holds a delegation, which delays reclamation. Record it when it happens; see [docs/validation.md](../../docs/validation.md).
