---
type: harvester
can-provide: [kubernetes, storage-server, nfs-client, iscsi-client, storage-server-reboot, multi-node, distinct-initiators, node-power-control, kubevirt, windows-guest-image]
creation: contributor-only
---

# harvester: SUSE Harvester on bare metal, storage server on the LAN

## Shape

A Harvester cluster (KubeVirt on RKE2, bare-metal nodes) and a separate Linux host on the same LAN running the lvmo-csi API. Bare-metal nodes have their own kernels and initiators, and run VMs natively.

Check that the Harvester version supports third-party CSI storage for VM disks before declaring `kubevirt` for lvmo volumes in the instance file.

Capabilities that depend on the instance:

- `node-power-control`: only if the instance names a way to power nodes off and on (IPMI/BMC, smart PDU) that the agent may use.
- `storage-server-reboot`: only if the storage host is dedicated to this lab.
- `windows-guest-image`: only if an image is already uploaded to Harvester.

## Preflight

- The kube context of the instance works with cluster-admin rights.
- The storage host answers SSH, `lvmo-api` is active, and every node can reach port 50051, 2049 and 3260 on it.

## Deploy a code change

Push a versioned driver image to the registry named in the instance file (ask first if none is named), then `helm upgrade` with that tag. Copy the API binary built for the storage host's architecture and restart `lvmo-api`.

## Create

Physical lab: provided by the contributor. The agent never creates or reinstalls it.

## Delete

Only what the agent installed: the Helm release, StorageClasses and test namespaces. Never touch the cluster or the hosts beyond that.
