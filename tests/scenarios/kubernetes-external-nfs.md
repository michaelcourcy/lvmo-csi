---
id: kubernetes-external-nfs
status: automated
groups: [basic]
requires: [kubernetes, storage-server, nfs-client]
automation: scripts/run-scenarios.sh kubernetes-external-nfs
---

# The driver passes the upstream Kubernetes storage e2e suite for NFS volumes

## Purpose

The Kubernetes project's external storage tests (`e2e.test`) check a CSI driver from the Kubernetes side: provisioning, mounting in pods, multiple pods and RWX, fsGroup, expansion, snapshots and cloning. The driver's capabilities are declared in [tests/external-nfs.yaml](../external-nfs.yaml), and the suite skips what the driver does not claim. This scenario covers NFS only; no iSCSI driver definition exists yet.

## Preconditions

- The driver is installed. StorageClass `lvmo-nfs` and VolumeSnapshotClass `lvmo-snapshots` exist.
- `e2e.test` for the cluster's Kubernetes version is installed (`scripts/setup-kind.sh` installs v1.35.0).
- The test needs cluster-wide permissions: use a cluster dedicated to testing.

## Steps

1. With the test cluster's kube context: `scripts/run-scenarios.sh kubernetes-external-nfs`. On Kind it runs `scripts/test-external.sh`; with `OPENSHIFT=true` it runs `scripts/test-external-pod.sh`, which runs the same suite from a pod inside the cluster. It focuses on `External.Storage.*lvmo.csi.io` and skips `[Disruptive]`, `[Serial]`, `[Slow]`, performance and stress tests.

## Expected

- Zero failed specs.
- The number of passed specs is reported. References from [docs/validation.md](../../docs/validation.md): 40 on Kind, 38 on OpenShift. A drop must be explained.

## Evidence

- The suite's summary line and the JUnit report written to `.test/reports/`.

## Cleanup

- The suite deletes its namespaces. Check that no `e2e-` namespace and no lvmo PV remains.
