# Snapshot metadata and backup

The implementation pins CSI 1.12.0 and external-snapshot-metadata 1.0.0. The local integration environment uses Kubernetes 1.35.0 and snapshot-controller 8.5.0. CSI and management APIs use gRPC/Protobuf. The public metadata API is the upstream authenticated gRPC/TLS service.

## Deployment

Install the upstream metadata CRD separately:

```sh
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshot-metadata/v1.0.0/client/config/crd/cbt.storage.k8s.io_snapshotmetadataservices.yaml
```

Create a TLS secret `lvmo-metadata-tls` in the driver namespace, with `tls.crt` and `tls.key`. The certificate must cover `lvmo-metadata.<namespace>.svc`. Enable the sidecar with `metadata.enabled=true` and set `metadata.caCert` to the base64-encoded trusted CA. `metadata.audience` defaults to `lvmo.csi.io`. Use your normal certificate issuance/rotation process in production. `scripts/enable-metadata.sh` creates a short-lived self-signed certificate for tests only.

The chart creates a `SnapshotMetadataService` named `lvmo.csi.io`, containing the endpoint address, CA, and audience. Backup clients discover this resource, request an audience-scoped service-account token, and send the token in each upstream request. The sidecar performs Kubernetes TokenReview and SubjectAccessReview checks; clients need `get` permission for VolumeSnapshots in the requested namespace. It resolves VolumeSnapshot names into CSI snapshot handles and calls the driver's Unix socket. The unauthenticated management API must remain on a trusted network; it is not the public backup endpoint.

## How ranges are produced

The backend uses **LVM thin pools**, not classic thick LVM snapshots. It reserves a kernel metadata snapshot with `dmsetup`, streams `thin_dump` XML, retains the selected thin-device maps, then releases the reservation. It never reads the entire volume to discover changes. A process mutex serializes metadata reservation with volume lifecycle operations; after metadata capture, response streaming and clone reads proceed independently.

Allocated ranges cover mapped blocks. Delta ranges compare logical mappings, physical block addresses, and mapping transaction times. They include mappings removed from the target: reads from those target ranges produce zeros. Adjacent ranges are merged and clipped to the requested starting offset and target capacity. Responses use `VARIABLE_LENGTH` metadata, bounded batches, deadlines, and cancellation. Metadata memory usage scales with the selected snapshots' mapping counts.

Full reconstruction starts from a newly truncated zero-filled sparse image. Incremental reconstruction starts from the verified previous image and overwrites **every** returned range, including zeroed/deallocated ranges. The backup client must preserve the base image, retain both snapshots until metadata/data processing completes, and avoid writing to backup clones. Offsets refer to the entire raw device, including filesystem metadata.

The independent `lvmo-metadata` client streams public API ranges, reads the corresponding iSCSI clone, applies ranges, and compares a SHA-256 hash of the reconstructed image against the entire target device. Full-device comparison is a test oracle, not the production change-detection mechanism. Its output file is intended as an acceptance-test artifact, not a complete production backup repository format.

## Snapshot consistency

For NFS, the server performs `sync -f` on the mounted source before taking the atomic thin snapshot. No filesystem freeze is held across external commands. The result is a crash-consistent block image; a filesystem restore may replay its journal. Writes still buffered on NFS clients or in applications are not covered. For iSCSI, the client must flush its writes if it requires them included in the snapshot; server-side snapshots cannot flush client application caches. Application-consistent backups need application quiescing by the backup orchestrator.

Raw block backup clones are never mounted or formatted. Filesystem restores retain the source filesystem and grow it if the requested capacity is larger. A filesystem clone can change protocol from NFS to iSCSI, or vice versa, within the same API endpoint and VG. A raw unformatted source cannot be restored as a filesystem. This CSI snapshot-clone capability does not establish that Kasten can restore an object-storage block export directly to NFS.

## Ordinary backups and Kasten

An unannotated Filesystem PVC continues to support an ordinary filesystem snapshot clone. Enabling the metadata sidecar does not change that behavior.

Kasten filesystem-to-block export is an independent opt-in:

- PVC: `k10.kasten.io/pvc-export-volume-in-block-mode: preferred` (allow fallback) or `force`.
- StorageClass: `k10.kasten.io/sc-supports-block-mode-exports: "true"`.
- For the NFS export-only path, annotate the source StorageClass with `k10.kasten.io/export-storage-class: <iscsi-class>`. This selects temporary export clones; it does not select the restore target.
- VolumeSnapshotContent used by the Block clone: `snapshot.storage.kubernetes.io/allow-volume-mode-change: "true"`. In the tested integration, Kasten sets this on its copied content.

Both classes must use `provisioner: lvmo.csi.io`; the clone must use the source API endpoint and VG. The clone's PVC dataSource references the source VolumeSnapshot in its namespace. Native Block PVCs do not need filesystem-conversion opt-in.

Kasten 9.0.7 block exports and object-storage restores passed for iSCSI Filesystem PVCs at both 340 million PostgreSQL rows and 272 million after deletion. The final application PVC remained Filesystem; the data mover used raw Block access. NFS block export through the alternate iSCSI class passed, but direct restore to NFS failed because its target cannot provide raw Block access. Use ordinary filesystem export for an NFS recovery workflow. Restoring an NFS-source block export onto iSCSI and manually converting its backend volume to NFS were not validated. See [the scenario](../tests/scenarios/kasten-block-mode-export.md).

These settings do not prove that a Kasten version consumes KEP-3314. Kasten CBT consumption and its `preferred` fallback behavior remain unvalidated. See [Kasten protection documentation](https://docs.kasten.io/latest/usage/protect/) and [export StorageClass configuration](https://www.veeam.com/kb4595).

## Test coverage

`tests/integration` runs real CSI operations against Linux and reconstructs full/incremental images for NFS, iSCSI filesystem, and iSCSI block sources. `scripts/test-metadata.sh` adds Kubernetes discovery, TLS, valid/invalid token handling, authorization rejection, filesystem-to-block restoration, stream continuation, and byte verification through the public sidecar. Native-block testing includes discard of a previously allocated region. `scripts/test-snapshots.sh` validates ordinary filesystem restores in the same namespace and across namespaces using the pinned upstream test manifests.
