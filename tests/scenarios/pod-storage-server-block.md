---
id: pod-storage-server-block
status: manual
groups: [basic]
requires: [kubernetes, nfs-client, iscsi-client, multi-node]
automation: none
---

# The pod storage server can back its VG with a Block-mode PVC

## Purpose

Validate `block-mode=true` on the [pod storage server](../../docs/pod-storage-server.md):
the VG is created directly on a Block-mode source PVC, with no loop device
and no source filesystem under LVM, while API state stays on a second
Filesystem PVC that the uninstall guard can still inspect. The source class
must bind `WaitForFirstConsumer`, so that both PVCs are placed together.
The default loop mode is covered by [pod-storage-server](pod-storage-server.md).

## Preconditions

- The same host, kernel and network prerequisites as
  [pod-storage-server](pod-storage-server.md), including privileged server Pods
  and trusted test clients.
- An existing non-lvmo StorageClass that supports Block volumes and binds
  `WaitForFirstConsumer` (for example EBS gp3), and another non-lvmo class, or
  a temporary one the run creates, that binds `Immediate`. Record their
  provisioners and binding modes.
- A driver release is installed with a VolumeSnapshotClass for `lvmo.csi.io`;
  the snapshot CRDs and controller are installed, and so is cert-manager (the
  charts default to mutual TLS on the API).
- The development server image is in the environment's permitted registry.
  No release or class name collides with this test.
- Record baseline loop devices, VGs, NFS exports and iSCSI targets on the nodes.

## Steps

1. Run `bash scripts/test-pod-storage-chart.sh`. It must pass, including the
   block-mode render checks: a Block PVC `<release>-storage`, a Filesystem PVC
   `<release>-storage-state`, the device at `/lvmo-dev/backing`, and both the
   server and the guard Job mounting the state PVC.
2. Against the cluster, run
   `helm install lvmo-block-reject charts/lvmo-csi-storage-server -n lvmo-block-test --create-namespace --dry-run=server --set source-storage-class=<immediate-class> --set block-mode=true`.
   It must fail with `block-mode requires a WaitForFirstConsumer source StorageClass`.
   Repeat without `--dry-run=server`: the install must fail and create no PVC,
   Deployment or StorageClass.
3. Install server release `lvmo-block` in namespace `lvmo-block-test` with the
   Block-capable class, `size=5Gi`, `block-mode=true`,
   `dest-storage-class-prefix=lvmo-block-sc` and the authorized image. Wait up to
   10 minutes for readiness. Verify:
   - PVC `lvmo-block-storage` has `volumeMode: Block` and
     `lvmo-block-storage-state` has `volumeMode: Filesystem`; both are Bound,
     and their PVs are in the server node's zone (or on its node, for a local
     class).
   - In the server, `pvs -o pv_name,vg_name` shows `/lvmo-dev/backing` as the
     only PV of the release VG; `losetup -l` shows no loop device for this
     release, and `/backing` holds no `disk.img`.
   - `vgs -o vg_name,autoactivation <vg>` shows auto-activation disabled.
   - `lvs <vg>/lvmo-pool` reports a thin pool of about 90% of the 5Gi device.
   - The classes `lvmo-block-sc-iscsi` and `lvmo-block-sc-nfs` exist.
4. Create namespace `lvmo-block-consumers`. Create a 256Mi Filesystem PVC on
   each generated class (NFS RWX, iSCSI RWO), mount each in a Pod on another
   node, write a 16Mi file within 5 minutes, record its SHA-256 and sync.
   Snapshot each PVC, wait up to 5 minutes for readyToUse, restore each to a
   new 256Mi PVC of the same class, and verify both hashes from that node.
5. Stop the consumer Pods cleanly, keep their PVCs, and delete the server Pod
   with graceful termination. Allow its replacement 10 minutes to become ready.
   Recreate the consumers and verify the original and restored hashes. Confirm
   that the VG UUID is unchanged and that no `pvcreate` or `vgcreate` ran in
   the new Pod's log.
6. Attempt server upgrades with `block-mode=false`, then with
   `state-size=2Gi`, keeping the other saved values. Each must fail with
   `backing identity is immutable` and leave the server Pod UID and VG UUID unchanged.
7. While consumers and snapshots exist, run
   `helm uninstall lvmo-block -n lvmo-block-test --wait --timeout 2m`. It must
   fail; the guard Job must run on the server's node and mount
   `lvmo-block-storage-state`. Data must stay readable.
8. Delete all consumer Pods, PVCs and snapshots, wait up to 10 minutes for
   reclamation, then retry the uninstall successfully. Both source PVCs must
   remain. Finish Cleanup.

## Expected

- An `Immediate` source class is refused in block mode before any resource
  is created; the same class is still accepted in loop mode.
- In block mode the VG's only PV is the Block PVC's device; no loop device or
  backing file is used, and the VG cannot be auto-activated by the host.
- API state, identity and the lock live on the Filesystem state PVC, which the
  uninstall guard inspects; both PVCs are placed in the server's zone or node.
- Both protocols provision, snapshot, restore and survive a graceful server
  replacement with exact file hashes, reusing the existing VG.
- `block-mode` and `state-size` cannot change on upgrade.
- Uninstall is refused while the backend is nonempty, and after reclamation it
  removes the workload, Service and classes, retaining both source PVCs.

## Evidence

- Versions, commit, rendered values, source class manifests, PVC and PV
  manifests with their topology, and the server node.
- `pvs`, `vgs`, `lvs` and `losetup -l` output from the server, before and
  after replacement, and the server log of both Pods.
- Refusal messages from steps 2, 6 and 7, PVC/snapshot handles and hashes.
- Cleanup inventory and proof that both source PVCs were retained, then deleted.

## Cleanup

- After success or failure, delete consumer Pods, snapshots and PVCs while the
  server runs, and wait for reclamation. Uninstall the server release.
- Delete both retained source PVCs explicitly and wait for their reclamation.
  Delete any temporary `Immediate` class and the test namespaces.
- Compare node inventory to the baseline; record leftovers as failures.

## Observations

- On Kind or minikube, whose default classes do not support Block volumes, a
  `block-mode=true` install leaves `<release>-storage` Pending. Record the
  provisioner's event if this is tried; loop mode is the supported setup there.

## Design notes

- The server mounts the host's `/dev`, and with that mount containerd does not
  create the container's `volumeDevices` node (observed on EKS). An init
  container without the mount receives the device and writes its major:minor
  to an `emptyDir`; the server recreates the node at `/lvmo-dev/backing`,
  outside `/dev`. LVM is told to scan `/lvmo-dev`; without that it does not
  see the device.
- `helm template` without `--dry-run=server` cannot look up the source class,
  so the binding-mode check applies only to installs and server-side renders.
