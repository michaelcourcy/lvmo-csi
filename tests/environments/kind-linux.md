---
type: kind-linux
can-provide: [kubernetes, nfs-client, iscsi-client]
creation: agent
---

# Kind on a dedicated Linux test host

## Shape

A Kind cluster inside a Linux host or VM with rootful Docker. The Helm test
storage server runs inside the cluster on a local-path backing PVC. The host
provides kernel NFS, loop, device mapper and LIO; all Kind nodes share that
kernel. This is a disposable functional test platform, not a resilience lab.

## Cannot provide

Distinct initiators with independent kernels, node power control, storage-server
reboot resilience and the dedicated-VM `test-storage-server` contract are not
provided. Do not run the pod-storage-server scenario's cross-node resilience
assumptions here; use its separate Kind quickstart scenario.

## Preflight

Check Docker and the VM are running, required kernel modules are available,
Kind nodes are Ready, and no unrelated NFS or iSCSI target server occupies the
host. Use explicit kube context `kind-lvmo-pod`.

## Deploy a code change

Build driver and server images inside the Linux host, load them with
`kind load docker-image --name lvmo-pod`, and upgrade the chart using the saved
values file. Development images remain local.

## Create

Follow the Kind quickstart in `docs/quickstart-kind.md`. On macOS, create a
separate Linux VM first; do not assume Docker Desktop provides server modules.
Record whether to retain the VM separately from the disposable Kind cluster.

## Delete

Remove consumer PVCs/snapshots before uninstalling the Helm release; explicitly
remove the retained backing PVC. Verify loop/VG/target cleanup before deleting
Kind. Keep or delete the Linux VM according to its instance lifecycle policy.
