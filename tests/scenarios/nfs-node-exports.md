---
id: nfs-node-exports
status: manual
groups: [basic]
requires: [kubernetes, nfs-client, multi-node]
automation: none
---

# An NFS volume is exported only to the nodes it is attached to

## Purpose

NFS uses AUTH_SYS: a client declares its user ID and the server believes it.
Exporting every volume to `*` let any pod that could reach TCP 2049 read and
write every NFS volume with a userspace NFS client, without privileges and
whatever `root_squash` setting. The API now exports each NFS volume only to the
addresses of the nodes it is attached to, as seen by the server on their
heartbeats, which take the same network path as their NFS mounts. ReadWriteMany
volumes are exported to every node they are attached to at once. Detaching a
node, or fencing it, removes its export.

## Preconditions

- A pod storage server installed from `charts/lvmo-csi-storage-server`, as in
  [api-mtls](api-mtls.md) (cert-manager, then the driver, then the server), or
  a standalone server for the variant below. Development images from the
  commit under test.
- Three schedulable worker nodes: `A`, `B` and `C`. Record their names and
  InternalIPs.
- Images `ubuntu:24.04` reachable, to install `libnfs-utils` (`nfs-ls`,
  `nfs-cat`, `nfs-cp`) in an attacker Pod.

## Steps

1. **Offline.** `go test ./internal/backend/` passes.
2. **Not exported before attach.** Create namespace `nfs-exp` and a 256Mi RWX
   PVC `shared` on the server's NFS class; wait until it is `Bound`. In the
   server (`exportfs -v`, or `/etc/exports.d/lvmo-<volume>.exports` on a
   standalone server), the volume is not exported to anyone.
3. **First node.** Run Pod `writer-a` on node `A` mounting `shared`. Within 3
   minutes it is `Ready`. The volume is exported to exactly one client: the
   address the server logged for node `A` (`node lvmo:<A>:… reaches this server
   from <address>`). Write a 16Mi random file `a.bin` and record its SHA-256.
4. **Multi-attach.** Run Pod `writer-b` on node `B` mounting the same PVC.
   Within 3 minutes it is `Ready`, and the volume is exported to both nodes'
   addresses. `writer-b` reads `a.bin` with the same hash and writes `b.bin`;
   `writer-a` reads `b.bin` with the same hash. Both Pods append 100 lines to
   one file `locked.txt` concurrently, each line under
   `flock /data/log.lock`; then each Pod counts 200 lines, 100 from each.
   Then run Pod `group-c` on node `C` with `runAsUser: 1000`, `fsGroup: 2000`
   (`restricted-v2` on OpenShift assigns both): it appends a line to `locked.txt`
   that `writer-a`, as root, created. The kubelet gives the volume the group
   `2000`; the append succeeds only if the server honours the client's groups
   (`manage-gids` off).
5. **Other clients are refused.** Run an unprivileged Pod `attacker` (default
   service account, no extra capabilities) on node `C`, with `libnfs-utils`.
   Against the server's NFS address (the Service IP for a pod server):
   1. `nfs-ls nfs://<address>/?version=4` and
      `nfs-ls -R nfs://<address>/<export path>?version=4`: record the output.
   2. `nfs-cat nfs://<address>/<export path>/a.bin?version=4&uid=0`,
      and the same with the owner's UID: fails, no content.
   3. `nfs-cp` of a local file to `nfs://<address>/<export path>/x.bin?version=4`:
      fails, and `x.bin` does not exist in the volume.
   4. Repeat 2 from an attacker Pod on node `A`, which the volume is attached
      to, and record the result: the Pod's own address is not the node's when
      the Pod network is not masqueraded towards the server.
6. **Detach.** Delete `writer-b`. Once its VolumeAttachment is gone (up to 5
   minutes), the volume is exported to node `A` only, and `writer-a` still reads
   and writes.
7. **Server restart.** Restart the server (delete its Pod; for the standalone
   variant, `systemctl restart lvmo-api`). Within 5 minutes the volume is
   exported to node `A` again and `writer-a` writes a new file, without being
   restarted.
8. **iSCSI unchanged.** A 256Mi RWO iSCSI PVC on the server's iSCSI class mounts
   in a Pod on node `B`, with a written file's hash read back.

## Expected

- A new NFS volume is exported to nobody until attached (step 2).
- An attached volume is exported to its nodes' addresses only, to several nodes
  at once for an RWX volume, and both nodes see each other's writes (steps 3–4).
- A Pod that is not root writes through its `fsGroup` (step 4).
- No call from the attacker Pod on node `C` reads or writes volume data (step 5).
- Detach removes only that node's export; a server restart restores the
  exports from its state (steps 6–7).
- iSCSI behaves as before (step 8).

## Evidence

- Node names and InternalIPs; the server's "reaches this server from" log lines.
- `exportfs -v` output after steps 2, 3, 4, 6 and 7.
- Pod placement, file hashes, `locked.txt` line counts, and the `group-c` append.
- Full outputs of every attacker call, with the attacker Pod's node and IP.

## Cleanup

- Delete namespace `nfs-exp` and wait for its PVs to be reclaimed. Remove the
  server and driver only if the run installed them.

## Variant: standalone storage server

Requires `storage-server` in addition. Use a standalone server whose API runs
without `--nfs-clients`, with StorageClasses for its private IP. Run the same
steps; in step 5, the NFS address is the server's IP and the export path is
`/var/lib/lvmo/volumes/<volume>`. On a cluster whose Pod network is masqueraded
to the node's address towards the server, step 5.4 is expected to succeed:
record it, it is the documented limit of node exports.

## Observations

- Unlocked concurrent appends from two nodes lose lines: an NFS client computes
  the end of the file itself, so two clients appending at once overwrite each
  other (180 of 200 lines on EKS). That is NFS semantics, hence `flock` in step 4.
- A probe that opened files leaves NFSv4 client state that the server keeps
  after the client is gone ("courteous server"), and it keeps the volume busy
  when it is deleted. After step 5, expire the attacker's clients
  (`echo expire > /proc/fs/nfsd/clients/<id>/ctl` for the `Libnfs` clients).

- With an overlay Pod network that masquerades traffic leaving the node, Pods
  on an attached node share its address and are not told apart.
- Fencing an NFS volume's node is covered by `TestNFSExportsFollowPublishedNodes`;
  record a failover run if one happens on the environment.
- The pod storage server's root export must not use `crossmnt`: with it, a
  client with no attachment could read, read-only, every volume through the
  root. The intermediate directory is exported with `nohide` instead.
