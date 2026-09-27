# Goal 

Build an LVM-backed CSI storage solution for Kubernetes, supporting NFS and iSCSI, with native snapshots and changed-block tracking for efficient backup and restore.

Contrary to Ceph or PX solution we're not looking for a highly resilient storage with multiple replication but a storage easy to backup and restore with solution like Kasten. Especially we will implement KEP-3314 for effcient crash consistent backup of large volume.

# Architecture 

The lvmo-csi API runs on a Linux machine and manages LVM storage independently of the protocol used to access it. It exposes services to:
- create LVM volumes and expose them through NFS exports or iSCSI targets
- extend volumes
- delete volumes and their exports or targets
- snapshot volumes
- create new volumes from snapshots
- retrieve allocated-block metadata and differences between snapshots

This API will be invoked by the CSI driver using gRPC with Protocol Buffers (Protobuf) binary serialization.

All project service APIs must use gRPC and Protobuf: the lvmo-csi management API, the standard CSI services, and the KEP-3314 snapshot metadata services. Define the lvmo-csi API in versioned `.proto` files and generate the Go client and server bindings. Use the upstream protocol definitions for CSI and snapshot metadata. Stream large allocated-block and changed-block metadata responses, with request deadlines, cancellation, and appropriate gRPC status codes.

The driver connects to the Linux server's lvmo-csi gRPC endpoint over TCP on the configured API port. Local CSI and sidecar connections use Unix domain sockets as required by their integration. The external snapshot metadata endpoint uses gRPC over TLS with the KEP-3314 authentication and authorization model. The existing decision to omit authentication between the driver and the lvmo-csi API still applies.

This requirement covers service API communication. Volume data access continues to use NFS or iSCSI, and Kubernetes resources are managed through the standard Kubernetes API.

For convenience in the rest of the prompt we will call this api "lvmo-csi API" 
and remove any ambiguity when pointing this component.

Those services will be consummed by a CSI driver on kubernetes. A storage class will hold the information to the lvmo-csi API. Each time the driver create a PVC a new LVM volume will be carved out from the volume groups the lvmo-csi API works on. 

The lvmo-csi API receive one or multiple Volume Group (VG) as an entry point. If the API has more than one VG then the storage class must specify the VG on which the LVM must be carved out.

The lvmo-csi API lives on a Linux machine, while the CSI driver runs on Kubernetes.

A single CSI driver identity supports both protocols. StorageClasses select the protocol through a `protocol: nfs` or `protocol: iscsi` parameter, in addition to the API endpoint and VG selection. NFS supports `Filesystem` volumes; iSCSI supports both `Filesystem` and raw `Block` volumes.

# Difference with NFS CSI 

A project NFS CSI driver maintained by the kubernetes community already exists where you provide an NFS share to the storage class and the driver create subfolder on this share each time you request a PVC. When you request a snapshot a tarball of the subfolder is created : https://github.com/kubernetes-csi/csi-driver-nfs.

This project is simple but has strong limitation : 
- defining a size on the PVC is pure decoration the real limit is the size of the underlying share exposed in the storage class
- snapshots are not crash consistent and are very unefficient. A tarball is a filesystem traversal and is managed by the client not the server. Beside there is no incremental approach each tarball is a full each time.

By using LVM volume in a VG instead of folders in a share we get 
- true size limitation (the os won't allow to overflow the LVM)
- true LVM snapshot (crash consistent)
- true CBT, it is possible to get diff between two LVM snapshot for efficient backup 

# Support KEP-3314

Implement [KEP-3314: CSI Changed Block Tracking](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md) to support efficient full and incremental backups with compatible backup clients. Driver-side KEP-3314 support is a required deliverable, independent of Kasten integration. Whether the targeted Kasten version consumes this API remains pending confirmation from the Kasten team; this must not block implementation or independent validation.

## Default filesystem backup and optional block export

Ordinary snapshot-based filesystem backup and restore must remain the default for `Filesystem` PVCs, including NFS volumes. The driver must support creating a filesystem clone from a snapshot so Kasten can mount it and export its files without raw block access or CBT. Adding KEP-3314 support must not force existing filesystem backup workflows to use iSCSI.

Block export is a separate path. According to the [Kasten backup documentation](https://docs.kasten.io/latest/usage/protect/), filesystem PVCs opt in using `k10.kasten.io/pvc-export-volume-in-block-mode: preferred` (allows filesystem fallback) or `force` (fails if block export is unavailable). An existing `Block` PVC does not need this filesystem-conversion opt-in. Kasten also requires the StorageClass annotation `k10.kasten.io/sc-supports-block-mode-exports: "true"` for block export.

For NFS-to-iSCSI backup clones, configure Kasten's export policy to select the iSCSI StorageClass using `exporterStorageClassName`, optionally through per-source-StorageClass overrides. Without an override, Kasten uses the source StorageClass. An existing iSCSI source can retain its compatible class. See [Kasten's export StorageClass configuration](https://www.veeam.com/kb4595).

Block export and CBT are distinct capabilities: block export does not itself guarantee CBT use. Confirm KEP-3314 consumption with the Kasten team before claiming Kasten CBT compatibility; the documented block-export settings do not establish that compatibility. Ordinary filesystem backup must work independently of the metadata service and the block-export configuration.

## Separate metadata from snapshot data access

KEP-3314 exposes allocated-block metadata for a snapshot and changed-block metadata between snapshots. It does not define an API for transferring the snapshot's data bytes; its documented backup workflow assumes that the backup application can read a volume created from the snapshot in raw `Block` mode.

The lvmo-csi API has access to the underlying LVM devices and metadata on the Linux server. It supplies the block metadata to the CSI driver, which exposes it through the CSI SnapshotMetadata service and the external-snapshot-metadata sidecar. The LVM implementation must provide an efficient, validated mechanism for obtaining these differences; this must not be assumed to work identically for classic LVM snapshots and thin snapshots.

An ordinary NFS export exposes files, not the underlying LVM device's byte address space. LVM block offsets therefore cannot be used directly to read files through NFS. For the optional block/CBT backup path, the snapshot's raw bytes must be made available separately through iSCSI, even when the original application volume uses NFS.

## Backup clones through iSCSI

Use two StorageClasses backed by the same CSI driver identity: an NFS class for NFS application volumes and an iSCSI class for block access. Kasten supports selecting an alternate StorageClass for its temporary backup volume and changing the volume mode from `Filesystem` to `Block`.

The optional CBT backup workflow, exercised by an independent test client and later by Kasten if compatibility is confirmed, is:

1. Create a VolumeSnapshot of the application PVC, whether it uses NFS or iSCSI.
2. Obtain allocated-block metadata for a full backup, or changed-block metadata relative to the previously backed-up snapshot for an incremental backup.
3. Create a temporary PVC whose `dataSource` references that VolumeSnapshot, whose `storageClassName` selects the iSCSI class, and whose `volumeMode` is `Block`.
4. The CSI driver asks the lvmo-csi API to create an LVM clone from the snapshot and expose that clone through iSCSI. The node component connects to the target and presents the raw block device to the backup pod.
5. The backup client reads the required byte ranges from the raw device using the snapshot metadata.
6. After backup, delete the temporary PVC and clean up its clone, iSCSI target, and sessions, without deleting the source volume or retained snapshot.

The target StorageClass's `provisioner` must match the source VolumeSnapshotContent's `spec.driver`. Using a different StorageClass is supported; restoring through an unrelated CSI driver is not part of this design. The driver must implement restoration across these protocol choices and accept the requested block access capability.

For a `Filesystem`-to-`Block` restore, the corresponding VolumeSnapshotContent must carry the annotation `snapshot.storage.kubernetes.io/allow-volume-mode-change: "true"`. The backup integration must ensure this permission is set. The clone retains the filesystem's raw bytes; changing the access mode must not format or mount the clone, or otherwise alter its contents before backup reads them.

## Required driver-side implementation and independent acceptance tests

Pin compatible versions of the CSI snapshot metadata API, external-snapshot-metadata sidecar, and Kubernetes components. Implement the driver-side `SnapshotMetadata` service, including `GetMetadataAllocated` and `GetMetadataDelta`, backed by real LVM snapshot metadata rather than mocks or full-volume comparison as the production mechanism. Honor the selected API version's streaming, continuation, validation, and error semantics.

Provide deployment support for the external-snapshot-metadata sidecar, service discovery through `SnapshotMetadataService`, TLS, and required RBAC. Document installation of the metadata CRD and other cluster-wide prerequisites separately, consistently with the chart's existing ownership rules. The metadata endpoint must enforce the sidecar's authentication and authorization model.

Add an independent integration test client to the automated test framework. It must exercise the public metadata endpoint through the sidecar and read snapshot clones as iSCSI raw block volumes, without requiring Kasten. Acceptance tests must:

- Discover the metadata service and verify authorized access and rejection of unauthorized requests.
- Retrieve allocated ranges for a full snapshot backup and changed ranges between snapshots after controlled writes, including overwrites and deallocation/zeroing where supported by the selected API and backend.
- Reconstruct the full snapshot image, apply an incremental backup, and verify the resulting bytes against the corresponding snapshot clone, including filesystem metadata. Define correct handling of unallocated and zeroed ranges so stale data cannot survive reconstruction.
- Cover NFS filesystem, iSCSI filesystem, and native iSCSI block source volumes, with raw block clones for metadata/data comparison.
- Exercise stream continuation, invalid snapshot requests, concurrent metadata queries and clone reads, and cleanup after interrupted operations.

Passing these tests establishes the driver's KEP-3314 functionality. It does not establish that a particular Kasten version consumes the service. Keep ordinary filesystem backup, raw-block export, and KEP-3314 integration as separately validated capabilities.

## Consistency and validation

The block offsets returned for a snapshot must identify the same bytes in the iSCSI clone, including filesystem metadata. Snapshot creation must define the server-side flushing/freezing behavior needed for crash consistency; it does not promise to capture writes still buffered on NFS clients or provide application consistency by itself.

Validate ordinary filesystem snapshot backup and restore without CBT for both NFS and iSCSI filesystem volumes. Separately validate Kasten raw-block export and restore for both protocols, including native block sources and filesystem sources opting into block export. Validate the CBT full-backup, incremental-backup, and restore workflow using an independent test client; add Kasten CBT integration tests once support and configuration are confirmed. Verify that unannotated filesystem PVCs retain the default filesystem export behavior and that `preferred` permits filesystem fallback when block export is unavailable. Verify restored data, metadata-to-clone offset correspondence, and cleanup after failures. Passing the standard CSI suites alone does not demonstrate this backup integration.

The decision to omit authentication between the CSI driver and the lvmo-csi API does not remove the authentication, authorization, and TLS requirements of the KEP-3314 external snapshot metadata service.

# Deployment 

## The api

The deployment of the api must be done using a single executable built for the main cpu/os architecture. 


An optional argument will redefine the default port this api listen to.

```
lvmo-csi [-P my-port] my-vg1 my-vg2
```

## The csi driver 

The csi driver will be deployed with a helm chart, it must supports deployment on openshift. 

The helm chart should not define the storage class neither the volumesnapshot class nor the volumesnapshot api, this is the responsability of the user or the responsability of the automated test and should be documented in the documentation of this project.

# Security model 

The security model is very limited and very close to the other NFS csi driver: end user don't have access to the storageclass spec neither the PV spec only the PVC spec for their own namespaces. 

 We don't want to implement an authentication mechanism between the csi driver and the lvmo-csi API. 

# Development 

The project will be in golang.

For the development platform we only target the Apple silicon platform but executable and docker image must support also AMD and ARM 64. 

For the developemnt and test on the kubernetes cluster use a Kind cluster. That you will create and delete as you need. 

For the development and test on the linux machine use LIMA.

Development is driven by the test, it consists on adding more and more csi capabilities and each time you select a consistent subset that you test end to end before you move to the next subset. 

# Artifact distribution and account access

The `gh` executable is available in my PATH. Use it with my existing GitHub authentication for repository and release operations. Always commit and push project changes directly to `main`; do not create feature branches or pull requests. Preserve existing remote changes and do not force-push.

You may use my existing Docker Hub configuration and authenticated credentials to push and pull this project's container images. Use the configured account's namespace and publish images for Linux AMD64 and ARM64 with versioned tags. Local test iterations may also load freshly built images directly into Kind.

You may use my GitHub account `michaelcourcy` and its existing authenticated configuration to publish this project's releases and upload release assets to the project repository. Include Linux AMD64 and ARM64 lvmo-csi executables and SHA-256 checksums. Use existing credentials without writing credentials into source files or logs.

For local testing on Lima, build the Linux executable for the VM's architecture (normally ARM64 on the Apple Silicon development machine), transfer it directly into the VM, and install/run it there. Automate this transfer in the local test setup; publishing a GitHub release is not required for each development iteration. The VM must also have the required LVM, filesystem, NFS, and iSCSI host dependencies installed.

For release validation or remote testing, including Azure, the setup may instead download a versioned executable from GitHub Releases using `curl`, verify its checksum, and install it. Make the artifact source selectable between a local build and a specified release version so test runs are reproducible.

# E2E Automated test 

Automatest test should validate test defined in : 
- csi-sanity 
- E2E test suite for External CSI driver 
- This simple suite test  https://github.com/michaelcourcy/test-csi-snapshot

But they should be defined in a global test framework and we should be able to invoke each one so that each time we could : 

- Locally 
    - create a LIMA instance 
    - create 2 VG groups on this instance
    - launch the lvmo-csi with the 2 VG group 
    - Create a new Kind cluster 
    - install the CSI driver 
    - configure the storage class and the snapshots class 
    - execute the 3 tests suites 
    - tear down the Lima machine and the Kind cluster 

- Azure 
    - use the current azure subscription
    - Create an ubuntu instance 
    - create 2 VG groups 
    - launch the lvmo-csi with the 2 VG group 
    - Use the current openshift cluster context deployed on azure 
    - install the CSI driver 
    - configure the storage class and the snapshots class 
    - execute the 3 tests suite 
    - tear down the unbuntu machine and uninstall the csi driver

 When not invoked separately all the test should be passed between set-up and tear-down to avoid useless duplication of environment.

 Locally or Azure is a parameter that we can pass along the name of the test. If no test names is passed and no environment name is passed then the default is local and it's all tests.

# License 

Choose the most adapted Open source license.
