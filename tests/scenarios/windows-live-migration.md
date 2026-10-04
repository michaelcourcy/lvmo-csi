---
id: windows-live-migration
status: proposed
groups: [kubevirt]
requires: [kubernetes, storage-server, iscsi-client, multi-node, distinct-initiators, kubevirt, windows-guest-image]
automation: none
---

# A Windows KubeVirt VM on an RWX Block iSCSI volume live-migrates without reboot or data loss

## Purpose

KubeVirt live migration needs the VM disk to be `ReadWriteMany` in `Block` mode: during the migration, the source and destination nodes have the disk open at the same time, and QEMU makes sure only one of them writes. lvmo must accept RWX for iSCSI Block volumes and still refuse it for iSCSI Filesystem volumes, where two nodes would corrupt the filesystem. This follows the same rule as Ceph RBD.

## Preconditions

- StorageClass `lvmo-iscsi`, with `allowVolumeExpansion: true`.
- The containerized data importer's (CDI) `StorageProfile` for `lvmo-iscsi` sets `claimPropertySets` to `accessModes: [ReadWriteMany]`, `volumeMode: Block`.
- A Windows image (DataSource, or a PVC in another namespace) is available, with the QEMU guest agent and OpenSSH server enabled, and an SSH key or credentials referenced in the environment instance.
- Live migration is allowed by the cluster's KubeVirt configuration.

## Steps

1. Create namespace `lvmo-vm-migration`.
2. Negative check: create a 1Gi PVC on `lvmo-iscsi` with `accessModes: [ReadWriteMany]` and `volumeMode: Filesystem`. Record its events after 60 s.
3. Create a VirtualMachine `win` with 2 vCPUs and 4Gi of memory, whose root disk is a DataVolume cloned from the Windows image onto `lvmo-iscsi`, `ReadWriteMany`, `Block`, 60Gi. Start it and wait up to 30 minutes for the guest agent to report the VM as connected.
4. In the guest, record `(Get-CimInstance Win32_OperatingSystem).LastBootUpTime`. Create `C:\lvmo\blob.bin` (500 MiB of random data) and record its SHA-256.
5. Start a guest-side writer that appends the current time to `C:\lvmo\ticks.log` every 200 ms.
6. Record the node running the VM (source). Create a `VirtualMachineInstanceMigration` for `win`. Wait up to 15 minutes for phase `Succeeded`.
7. Record the node now running the VM (target). Stop the writer.
8. In the guest, record the boot time again, recompute the SHA-256 of `blob.bin`, and find the longest gap between consecutive lines of `ticks.log`.
9. Migrate back to the source node and repeat step 8.
10. Run `chkdsk C:` in read-only mode (`chkdsk C: /scan`) and record the result.

## Expected

- Step 2: the PVC stays `Pending` and its events show that multi-node access is refused for iSCSI Filesystem volumes.
- Steps 6 and 9: both migrations reach `Succeeded`, and the target node differs from the source node each time.
- The boot time never changes: the guest did not reboot.
- The checksum of `blob.bin` never changes.
- `ticks.log` has no gap longer than 10 s.
- `chkdsk` reports no problems.
- After each migration, the storage server shows the volume accessed only by the node now running the VM.

## Evidence

- PVC events from step 2.
- Both `VirtualMachineInstanceMigration` objects with their phase and timestamps.
- Boot times, checksums and the longest `ticks.log` gaps, before and after each migration.
- `chkdsk` output.
- The volume's ownership and iSCSI sessions on the storage server before, during (if observable) and after each migration.

## Cleanup

- Stop and delete `win`, its DataVolume and the PVC from step 2. Delete namespace `lvmo-vm-migration`.
- Check on the storage server that no LV, target, session or ownership entry remains.

## Observations

- Migration duration and the guest-measured pause, for future comparison.

## Design notes

- `validate` accepts `MULTI_NODE_MULTI_WRITER` for iSCSI only with Block access. Filesystem stays refused.
- Ownership becomes a set of nodes for RWX Block volumes. Each node is added on stage and removed on unstage, without affecting the others.
- If target-side fencing exists (see `iscsi-takeover-after-node-failure`), the target ACL lists every node in the set.
- The driver must not format, mount or otherwise change a Block volume on any node.
