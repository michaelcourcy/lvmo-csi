# Prepare the Kubernetes nodes

This guide prepares the worker nodes of a cluster for lvmo volumes, before you install the Helm chart. It is written for the person who manages the nodes' operating system: images, MachineConfigs, or bootstrap scripts.

A node needs one thing from its operating system: the iSCSI daemon, `iscsid`, from the `open-iscsi` package. Everything else comes with the node plugin, which checks each node when it starts ([Check](#check)). The chart never installs packages on the nodes: their operating system is yours to manage.

## What a node needs

| Requirement | Who provides it | For |
|---|---|---|
| `iscsid` running on the host | **You**: the `open-iscsi` or `iscsi-initiator-utils` package, with `iscsid` enabled. The node plugin uses host networking and the host's daemon to log in to targets | iSCSI volumes |
| The `iscsi_tcp` kernel module | The node plugin loads it on the host when it starts. The host's kernel must include it, as distribution kernels do | iSCSI volumes |
| A unique initiator name | The node plugin: see [Initiator names](#initiator-names) | iSCSI volumes |
| `iscsiadm`, the NFS mount helper, `mkfs.ext4`, `mkfs.xfs` | The node plugin's image | Both |
| TCP to the storage server on ports 50051, 2049 and 3260 | **You**: the network between nodes and storage server | Both |

The NFS client package (`nfs-common` or `nfs-utils`) is not needed, since the image has the mount helper, but every tested environment had it, so the commands below install it.

A cluster that uses only NFS volumes needs nothing on its nodes. Install the chart with `--set nodeCheck.iscsi=false` so that the check does not block nodes without `iscsid`.

## By platform

### Ubuntu and Debian

```sh
sudo apt-get install -y open-iscsi nfs-common
sudo systemctl enable --now iscsid
```

### RHEL, Rocky Linux, AlmaLinux, Amazon Linux 2023

```sh
sudo dnf install -y iscsi-initiator-utils nfs-utils
sudo systemctl enable --now iscsid
```

### SUSE and SLE Micro (RKE2, Harvester)

```sh
sudo zypper install -y open-iscsi nfs-client
sudo systemctl enable --now iscsid
```

On a transactional system such as SLE Micro, install with `transactional-update pkg install open-iscsi nfs-client` and reboot. Harvester nodes already ship `open-iscsi` for Longhorn.

### OpenShift

Red Hat CoreOS includes `iscsi-initiator-utils` but does not enable `iscsid`. Enable it with a MachineConfig:

```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 99-worker-lvmo-iscsid
  labels:
    machineconfiguration.openshift.io/role: worker
spec:
  config:
    ignition:
      version: 3.2.0
    systemd:
      units:
      - name: iscsid.service
        enabled: true
```

Applying it reboots the workers one at a time. Use a MachineConfig with the `master` role as well if workloads run on control-plane nodes.

### Amazon EKS

Put the commands for the node group's operating system in its bootstrap. With `eksctl` and Ubuntu nodes:

```yaml
  preBootstrapCommands:
  - apt-get update -qq
  - DEBIAN_FRONTEND=noninteractive apt-get install -y -qq open-iscsi nfs-common
  - systemctl enable --now iscsid
```

With Amazon Linux 2023, use the RHEL commands in the node group's user data. Bottlerocket was not tested.

### Other platforms

- **Talos**: add the `siderolabs/iscsi-tools` system extension, which runs `iscsid`. Not tested: the check tells whether the node plugin can use it.
- **Kind**: Kind nodes share the host's kernel and have no iSCSI daemon of their own. Use NFS volumes and `nodeCheck.iscsi=false`, as in the [laptop walkthrough](../tests/environments/lima.md). The project's nested tests use a special setting, `iscsiHostProc`, that is not for real clusters.

## Initiator names

The storage server identifies a node by its iSCSI initiator name. It admits only the initiators of the nodes a volume is attached to, and fences a failed node by removing its initiator. Two nodes with the same name could both reach a volume, and fencing one would cut off the other.

The host's own name, in `/etc/iscsi/initiatorname.iscsi`, is often shared: nodes created from one disk image or VM template all inherit the name generated when the image was built. So lvmo does not use it. The node plugin generates a name of its own the first time it starts on a node, such as `iqn.2026-09.io.lvmo.node:worker-1:3f9a0c2e41b87d65`: the node name for readability, and a random part, because node names come back when nodes are replaced. It keeps the name in `/var/lib/lvmo-node/initiatorname` on the host, so that it survives pod restarts, upgrades and reboots, and logs in through an open-iscsi interface record (`iscsiadm -m iface`) named `lvmo-…` that carries this name. The host's default name and any other iSCSI user on the node are left alone.

Two consequences:

- **Do not copy `/var/lib/lvmo-node` between nodes**, for example by building an image from a node that ran lvmo. The check refuses a node whose name another node already uses.
- **The name changes if `/var/lib/lvmo-node` is deleted**, or a node is reinstalled. Drain the node first: volumes attached under the old name can no longer be reached from it.

To see the names, on the storage server: `targetcli ls /iscsi` lists each target's allowed initiators. In the cluster: `kubectl get csinodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.drivers[?(@.name=="lvmo.csi.io")].nodeID}{"\n"}{end}'`, where each node ID is `lvmo:<node>:<initiator>`.

## Multipath

If `multipathd` runs on the nodes, it may claim lvmo's iSCSI disks, and then formatting or mounting them fails with "device busy". This was not tested. If you see it, exclude the storage server's disks in `/etc/multipath.conf` (their SCSI vendor is `LIO-ORG`), or disable `multipathd` if nothing else needs it.

## Check

With `nodeCheck.iscsi` enabled (the default), each `lvmo-node` pod first runs an init container, `node-check`. It:

- checks that `iscsid` answers on its control socket;
- loads the `iscsi_tcp` module, if it is not loaded yet;
- creates lvmo's initiator name, if the node has none yet;
- checks that no other node running lvmo has the same initiator name.

A node that fails keeps its `lvmo-node` pod in `Init:Error` or `Init:CrashLoopBackOff`, so no lvmo volume is used on it until it is fixed. The init container's log says what is missing:

```sh
kubectl -n lvmo-system get pods -l app=lvmo-node -o wide
kubectl -n lvmo-system logs <lvmo-node-pod> -c node-check
```

```text
node worker-2 cannot serve lvmo iSCSI volumes:
- iscsid is not reachable (dial unix @ISCSIADM_ABSTRACT_NAMESPACE: connect: connection refused): install open-iscsi on the host and enable iscsid
- see docs/nodes.md, or set nodeCheck.iscsi=false if this cluster uses only NFS volumes
```

Kubernetes retries the init container, so the pod starts by itself once the node is fixed. To retry at once, delete the pod.

The duplicate check compares the node with the other nodes' published node IDs. Two nodes that start at the same moment can miss each other: the check catches them the next time either pod restarts. If the check cannot read the other nodes, it logs a warning and passes.
