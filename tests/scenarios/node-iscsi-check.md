---
id: node-iscsi-check
status: manual
groups: [basic]
requires: [kubernetes, storage-server, iscsi-client, multi-node, distinct-initiators]
automation: none
---

# Each node uses its own lvmo initiator name, and the node plugin refuses a node that cannot serve iSCSI volumes

## Purpose

The storage server admits and fences nodes by initiator name. The host's name, in `/etc/iscsi/initiatorname.iscsi`, is shared by nodes cloned from one image, which silently defeats access control and fencing, and depends on how each distribution builds its nodes. lvmo therefore generates its own name once per node, keeps it in `/var/lib/lvmo-node/initiatorname`, and logs in through its own open-iscsi iface record, leaving the host's name alone. It also loads `iscsi_tcp` on the host itself. What remains for the host is `iscsid`: the node plugin checks it before it starts (chart value `nodeCheck.iscsi`, on by default), so that a node without it is visible at installation, with the fix. See [docs/nodes.md](../../docs/nodes.md).

## Preconditions

- The driver installed from a build that includes this behaviour, with default values (`nodeCheck.iscsi` unset), after removing any earlier installation and its volumes.
- StorageClass `lvmo-iscsi`.
- At least two workers, `worker-a` and `worker-b`, with `open-iscsi` installed and `iscsid` enabled, and a root shell on each (`kubectl debug node/<name> -it --profile=sysadmin --image=busybox -- chroot /host`).
- Record each worker's `/etc/iscsi/initiatorname.iscsi`.

## Steps

1. `kubectl -n lvmo-system get pods -l app=lvmo-node -o wide`: every pod is `Running`. Save each pod's `node-check` log, each worker's `/var/lib/lvmo-node/initiatorname`, and the node IDs: `kubectl get csinodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.drivers[?(@.name=="lvmo.csi.io")].nodeID}{"\n"}{end}'`.
2. **Hosts sharing one name.** On `worker-b`, back up `/etc/iscsi/initiatorname.iscsi`, write `worker-a`'s content into it, and run `systemctl restart iscsid`, as a node cloned from `worker-a`'s image would be.
3. Create namespace `lvmo-nodecheck` and two 1Gi RWO `Filesystem` PVCs on `lvmo-iscsi`, `a` and `b`, each used by a pod pinned with `nodeName` to `worker-a` and `worker-b`, writing a line to `/data/log`. Wait for both pods to be Ready (180 s).
4. On each worker: `iscsiadm -m session -P 1` shows the volume's target with `Iface Name: lvmo-…` and `Iface Initiatorname:` equal to the worker's `/var/lib/lvmo-node/initiatorname`. On the storage server: `targetcli ls /iscsi/<target of a>/tpg1/acls` lists only `worker-a`'s lvmo name, and the same for `b` and `worker-b`.
5. Delete pod `b`, then from `worker-b` try to log in to volume `a`'s target with the host's default iface: `iscsiadm -m discovery -t sendtargets -p <server>:3260` then `iscsiadm -m node -T <target of a> -p <server>:3260 -I default --login`. Restore `worker-b`'s `/etc/iscsi/initiatorname.iscsi` from the backup and restart `iscsid`. Delete the node records the attempt created (`iscsiadm -m node -T <target of a> -I default -o delete`).
6. **Stable name.** Delete `worker-b`'s `lvmo-node` pod. When the new one is `Running`, its `node-check` log and node ID show the same initiator as in step 1.
7. **Module loaded by lvmo.** On `worker-b`, with no lvmo volume attached to it: `modprobe -r iscsi_tcp` (stop `iscsid` first if the module is in use, then start it again). Delete `worker-b`'s `lvmo-node` pod. Within 2 minutes the new pod is `Running`, and `lsmod | grep ^iscsi_tcp` on `worker-b` shows the module.
8. **iscsid not running.** On `worker-b`: `systemctl stop iscsid iscsid.socket`. Delete the pod. Within 2 minutes the new pod is in `Init:Error` or `Init:CrashLoopBackOff`; save `kubectl logs <pod> -c node-check`. Then `systemctl start iscsid.socket iscsid`, and wait up to 5 minutes, without deleting the pod, for it to become `Running` by itself.
9. **Copied lvmo name.** On `worker-b`, back up `/var/lib/lvmo-node/initiatorname` and write `worker-a`'s into it. Delete the pod; check that it fails as in step 8, and save the log. Restore the backup, delete the pod, and check that it becomes `Running`.
10. **Opt-out.** `helm upgrade lvmo charts/lvmo-csi -n lvmo-system --reset-then-reuse-values --set nodeCheck.iscsi=false --wait`. Check that the DaemonSet has no init container (`kubectl get ds lvmo-node -n lvmo-system -o jsonpath='{.spec.template.spec.initContainers}'` prints nothing) and that every `lvmo-node` pod is `Running`. Then upgrade again with `--set nodeCheck.iscsi=true`.

## Expected

- Step 1: every `node-check` log ends with `node <name> is ready for lvmo iSCSI volumes (initiator <name>)`. Each initiator starts with `iqn.2026-09.io.lvmo.node:<node name>:`, is different on each worker, and equals that worker's `/var/lib/lvmo-node/initiatorname` and the initiator in its node ID. No worker's `/etc/iscsi/initiatorname.iscsi` changed.
- Steps 3 and 4: both pods are Ready although the two hosts share one host name. Each session uses lvmo's iface and the worker's lvmo name, and each target admits only its own node's lvmo name.
- Step 5: the login with the host's default name is refused (`iscsiadm` fails with an authorization error, or discovery lists no target): the host's name is not in any lvmo access list.
- Step 6: the initiator is unchanged after the pod restart.
- Step 7: the pod starts without manual action and the module is loaded.
- Step 8: the log says `iscsid is not reachable` and ends with the line pointing to `docs/nodes.md` and `nodeCheck.iscsi=false`. The pod's `driver` container never started while `iscsid` was stopped, and the pod became `Running` by itself after.
- Step 9: the log names the shared initiator and `worker-a`.
- Step 10: no init container while `nodeCheck.iscsi=false`; the pods are `Running` on every node.
- Pods on the other nodes stay `Running` throughout.

## Validation

Passed on `eks-paris` on 2026-10-05 (3 Ubuntu 24.04 workers, EKS 1.35): every step as expected; the login with the shared host name was refused with an authorization failure, and the node plugin started by itself 50 s after `iscsid` came back.

## Evidence

- The pod listings, `node-check` logs and node IDs of steps 1, 6, 8 and 9.
- The `iscsiadm -m session -P 1` output of both workers and the `targetcli` ACL listings of step 4.
- The output of the refused login in step 5.
- `lsmod` on `worker-b` in step 7, and the `initContainers` output of step 10.

## Cleanup

- Namespace `lvmo-nodecheck` deleted, and no session or ACL left for its volumes.
- `worker-b`'s `/etc/iscsi/initiatorname.iscsi` and `/var/lib/lvmo-node/initiatorname` restored, `iscsid` running, `node-debugger-*` pods deleted.
- The chart back to `nodeCheck.iscsi=true`, or to the values it had before the run.

## Design notes

- The name is generated by the node plugin, not taken from the host or derived from the node name alone: node names come back when nodes are replaced, and a replacement must not inherit the access of a node that may have been fenced.
- lvmo's iface record is named after its initiator, so that nested test nodes sharing one iSCSI database each get their own.
- The check is an init container running `lvmo-driver --check-node`, so that a failing node shows `Init` in `kubectl get pods` and the reasons in one log. It connects to iscsid's abstract control socket, `@ISCSIADM_ABSTRACT_NAMESPACE`, as `iscsiadm` does.
- The module is loaded with `nsenter --target=1 --mount -- modprobe iscsi_tcp`, in the host's mount namespace: the node plugin shares the host's PID namespace.
- Failing to read the other nodes' initiators is a warning, not a failure, so that a node is never blocked by the API server.
