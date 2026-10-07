Stop using iSCSI sendtargets discovery in NodeStageVolume (internal/driver/node.go). Create only the volume's own node record (iscsiadm -m node -T <iqn> -p <portal> -I <lvmo iface> -o new), then update and log in as now. Discovery leaves stale records of other targets on every node. That's harmless with node.startup = manual, but a distro that defaults to automatic would try them all at boot.
- Check that -o new is idempotent when a failed stage is retried.
- Add a "no stale node record after unstage" check to node-iscsi-check or cleanup-audit.
- Validate on eks-paris. That means recreating the storage server and ECR, then deleting them.


Kasten block-mode export for iSCSI and NFS lvmo volumes: [kasten-block-mode-export](../tests/scenarios/kasten-block-mode-export.md) was exercised on `eks-paris`: both iSCSI block export/restore generations passed, and NFS block export via iSCSI passed. Direct restore to NFS is blocked because Kasten tries raw Block access on the NFS target. The original direct-NFS-restore requirement is unsupported; restoring onto iSCSI Filesystem PVCs would be a different workflow. The scenario now documents this as a limitation check, not an NFS recovery workflow. Use ordinary filesystem export for NFS recovery; manual LV rebinding is unvalidated and out of scope. The NFS after-deletion case has not run.

Kasten use of changed block tracking (KEP-3314): the driver side is done and automated. Kasten consumption remains unvalidated; confirm support and configuration with the Kasten team before claiming incremental block exports based on these ranges.

iSCSI Block + ReadWriteMany for KubeVirt live migration: windows-live-migration is still proposed, and the controller still refuses multi-node access modes for iSCSI.
