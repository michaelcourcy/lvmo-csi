---
type: ocp-azure
can-provide: [kubernetes, openshift, storage-server, test-storage-server, nfs-client, iscsi-client, storage-server-reboot, multi-node, distinct-initiators, node-power-control, kubevirt, windows-guest-image, kasten]
creation: contributor-only   # the agent may create the storage server VM, never the cluster
---

# ocp-azure: OpenShift on Azure with an Ubuntu storage VM

## Shape

An existing OpenShift cluster on Azure, and an Ubuntu 24.04 VM in the same virtual network running the lvmo-csi API. Each worker is a separate Azure VM, so `multi-node` and `distinct-initiators` hold.

Capabilities that depend on the instance, and must be declared there explicitly:

- `kubevirt`: OpenShift Virtualization installed, and worker VM sizes that support nested virtualization (or bare-metal workers).
- `node-power-control`: the agent may run `az vm stop` / `az vm start` on named workers. Pause MachineHealthChecks first, or the Machine API will replace the stopped node.
- `test-storage-server`: only for a storage VM created by `scripts/e2e-azure.sh`, which installs the direct CSI endpoint and the test VGs.
- `windows-guest-image`, `kasten`: only if already installed.

## Preflight

- `kubectl config current-context` matches the instance; `oc whoami` works with cluster-admin rights.
- `az account show` shows the subscription that owns the cluster.
- The storage VM answers SSH, `lvmo-api` is active, and every node can reach port 50051, 2049 and 3260 on it.

## Deploy a code change

Build the driver image inside the cluster and store it in OpenShift's internal registry, as `scripts/e2e-azure.sh` does (`oc new-build --binary --strategy=docker`, then `oc start-build --from-dir`), then `helm upgrade` with `image.repository=image-registry.openshift-image-registry.svc:5000/lvmo-system/lvmo-csi`. No external registry and no pull secret are needed. Copy the Linux AMD64 API binary to the storage VM and restart `lvmo-api`.

## Create

The cluster is provided by the contributor. The storage server VM can be created by the agent: `scripts/e2e-azure.sh` shows how (resource group, VM in the cluster's subnet, storage preparation, API installation), and refuses to run over an existing lvmo installation.

## Delete

Only what the agent created: uninstall the Helm release and the StorageClasses it created, then delete the storage VM's resource group. Never delete the cluster.
