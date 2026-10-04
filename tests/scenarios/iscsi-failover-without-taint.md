---
id: iscsi-failover-without-taint
status: manual
groups: [resilience]
requires: [kubernetes, storage-server, iscsi-client, multi-node, distinct-initiators, node-power-control]
automation: none
---

# A pod and its iSCSI volume move to another node on their own when the node dies

## Purpose

Administrators expect a workload on a failed node to restart elsewhere without anyone tainting the node. Kubernetes does not do this by itself for a single-node volume: the old pod stays `Terminating` and the volume stays attached to the dead node. lvmo detects the failure from two independent signals, fences the node at the storage server, then deletes the stranded pods that use lvmo volumes, so that Kubernetes reschedules them and moves their volumes. A node that is alive but unreachable cannot corrupt the volume, because it is fenced before anything moves.

## Preconditions

- StorageClass `lvmo-iscsi`, driver installed with failover enabled (the default) and its default timeout of 120 seconds.
- Every worker has a distinct iSCSI initiator name.
- No MachineHealthCheck or Auto Scaling replacement will replace the node during the test, or it is paused.

## Steps

1. Create namespace `lvmo-failover`, a 1Gi RWO `Filesystem` PVC `data` on `lvmo-iscsi`, and a single-replica Deployment appending a timestamp line to `/data/log` every second. Wait for Ready (180 s). Call its node A.
2. After 30 s, record the last line of `/data/log`.
3. Power node A off (not a graceful shutdown). Record the time. Do not taint it.
4. Wait up to 15 minutes for a replacement pod to be Ready on another node, node B. Record the time.
5. Read `/data/log` from the new pod.
6. Record the volume's attachment, ownership and target ACL on the storage server, and the driver controller's log.
7. Power node A back on. From node A, try to log in to the volume's target.
8. Repeat with the storage server unreachable from node A but node A powered on: block port 50051 and 3260 from node A only, so that its kubelet stays Ready. After 5 minutes, check that the pod has not moved. Remove the block.

## Expected

- Step 4: the replacement pod is Ready on node B within 15 minutes of the power-off (detection, plus Kubernetes' 6-minute forced detach), with no manual action.
- The log written before the failure is intact.
- The storage server lists only node B as allowed to reach the volume, and the controller log shows node A being fenced before its pods were deleted.
- Step 7: node A's login is refused.
- Step 8: a node whose kubelet is still Ready is never fenced, even if it cannot reach the storage server.

## Evidence

- Times of power-off, fencing, pod deletion, attachment and readiness.
- Storage server ACL, ownership and attachments before and after.
- The controller's failover log lines.
- Node A's login attempt.

## Cleanup

- Delete namespace `lvmo-failover`. Remove any network block. Check on the storage server that nothing remains.

## Validation

Passed on `eks-paris` on 2026-10-03 with image `dev-iscsi-failover-2`: after a forced power-off, the node was fenced and its pod deleted 2m54s later, and the pod was Ready on another node 9m02s after the power-off with its data intact. The fenced node's login was refused when it came back, and a Ready node cut off from the storage server for 5 minutes was left alone.

## Design notes

- A node is declared dead only when both signals have been lost for the timeout: Kubernetes reports it not Ready, and its lvmo node plugin has stopped sending heartbeats to the storage server. The storage server, which receives the heartbeats, refuses to fence a node it heard from within the timeout.
- Fencing removes the node from every target it is attached to, closes its sessions and releases its ownership, before any pod is deleted.
- Only pods using lvmo volumes are force-deleted. Kubernetes then detaches the volume from the dead node after its built-in 6-minute timeout, unless the cluster disables forced detach on timeout. Other volumes and pods on the node are left to Kubernetes.
- Pods using lvmo NFS volumes are moved too, because the attach step would otherwise strand single-node NFS volumes on the dead node. They are not fenced: the storage server cannot fence an NFS client.
- Failover is on by default and can be disabled in the chart (`failover.enabled=false`).
