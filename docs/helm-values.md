# Helm values reference

Every value of the two charts, with its default and a link to the section that
explains it. Set them with `--set name=value` or a values file. With
`--reuse-values` upgrades, values added by a newer chart take the defaults
below.

## Driver chart (`lvmo/lvmo-csi`)

| Value | Default | What it does | Details |
|---|---|---|---|
| `image.repository` | `michaelcourcy/lvmo-csi` | Driver image | [Images](#images) |
| `image.tag` | empty: the chart's appVersion | Driver image tag | [Images](#images) |
| `image.pullPolicy` | `IfNotPresent` | Pull policy of the driver image | [Images](#images) |
| `apiEndpoint` | empty | Default storage server API for volumes created before StorageClasses named their `endpoint`, and for standalone CSI tests. New StorageClasses set `endpoint` instead | [Kubernetes / OpenShift](../README.md#kubernetes--openshift) |
| `apiTLS.enabled` | `true` | Mutual TLS to every storage server's API. Needs cert-manager unless `apiTLS.existingSecret` is set. `false` only on an isolated test network, together with `tls.enabled=false` on the servers | [Mutual TLS](pod-storage-server.md#mutual-tls), [Without TLS](pod-storage-server.md#without-tls) |
| `apiTLS.issuer` | `lvmo-csi-api` | Name of the ClusterIssuer the chart creates, and prefix of its CA resources. Servers must use the same value in `tls.issuer` | [Mutual TLS](pod-storage-server.md#mutual-tls) |
| `apiTLS.certManagerNamespace` | `cert-manager` | cert-manager's cluster resource namespace, where the server CA is created | [Mutual TLS](pod-storage-server.md#mutual-tls) |
| `apiTLS.existingSecret` | empty | A Secret of your own (`tls.crt`, `tls.key`: driver certificate; `ca.crt`: the CA of the servers) instead of cert-manager | [Without cert-manager](storage-server.md#give-the-api-its-certificate) |
| `kubeletRoot` | `/var/lib/kubelet` | The kubelet's root directory on the nodes | [Kubelet directory](#kubelet-directory) |
| `openshift` | `false` | `true` creates the SecurityContextConstraints the driver needs on OpenShift | [Kubernetes / OpenShift](../README.md#kubernetes--openshift), [OpenShift nodes](nodes.md#openshift) |
| `metadata.enabled` | `false` | Serve changed block tracking (snapshot metadata) through the `csi-snapshot-metadata` sidecar | [Snapshot metadata deployment](metadata.md#deployment) |
| `metadata.image` | `registry.k8s.io/sig-storage/csi-snapshot-metadata:v1.0.0` | Sidecar image | [Snapshot metadata deployment](metadata.md#deployment) |
| `metadata.tlsSecret` | `lvmo-metadata-tls` | TLS Secret of the metadata endpoint | [Snapshot metadata deployment](metadata.md#deployment) |
| `metadata.audience` | `lvmo.csi.io` | Token audience backup clients request | [Snapshot metadata deployment](metadata.md#deployment) |
| `metadata.caCert` | empty, required with `metadata.enabled` | Base64 CA of the metadata endpoint, published in the `SnapshotMetadataService` | [Snapshot metadata deployment](metadata.md#deployment) |
| `sidecars.provisioner`, `sidecars.attacher`, `sidecars.snapshotter`, `sidecars.resizer`, `sidecars.registrar` | the upstream `registry.k8s.io/sig-storage` images the release was tested with | Kubernetes CSI sidecar images | [Images](#images) |
| `nodeCheck.iscsi` | `true` | Check each node's iSCSI prerequisites before its node plugin starts. `false` only when the cluster uses NFS volumes alone | [Check](nodes.md#check), [What a node needs](nodes.md#what-a-node-needs) |
| `iscsiHostProc` | empty | Only for nested Kind tests: the host's `/proc`, to use the host's `iscsid`. Leave empty on real nodes | [Create the cluster and install lvmo](quickstart-kind.md#create-the-cluster-and-install-lvmo) |
| `failover.enabled` | `true` | Move workloads off a failed node without an out-of-service taint, after fencing it at the storage servers | [Kubernetes / OpenShift](../README.md#kubernetes--openshift) |
| `failover.timeout` | `120s` | How long Kubernetes and the storage servers must both have lost a node before it is fenced | [Kubernetes / OpenShift](../README.md#kubernetes--openshift) |
| `snapshotClass.enabled` | `true` | Create the shared VolumeSnapshotClass. Needs the snapshot CRDs | [Snapshot CRDs](pod-storage-server.md#snapshot-crds), [Shared snapshot class](pod-storage-server.md#shared-snapshot-class) |
| `snapshotClass.name` | `lvmo-snapshots` | Its name | [Shared snapshot class](pod-storage-server.md#shared-snapshot-class) |
| `snapshotClass.deletionPolicy` | `Delete` | `Delete` or `Retain` | [Shared snapshot class](pod-storage-server.md#shared-snapshot-class) |
| `snapshotClass.kasten` | `true` | Annotate it `k10.kasten.io/is-snapshot-class: "true"` | [Shared snapshot class](pod-storage-server.md#shared-snapshot-class) |

## Storage-server chart (`lvmo/lvmo-csi-storage-server`)

A privileged, single-replica **test** server backed by a PVC.

| Value | Default | What it does | Details |
|---|---|---|---|
| `source-storage-class` | required | Existing, non-lvmo StorageClass of the backing PVC | [Resources and data path](pod-storage-server.md#resources-and-data-path) |
| `size` | `50Gi` | Size of the backing PVC; usable capacity is smaller | [Resources and data path](pod-storage-server.md#resources-and-data-path), [Value formats](pod-storage-server.md#value-formats) |
| `block-mode` | `false` | `true` uses a Block-mode PVC as the LVM physical volume instead of a loop file | [Block mode](pod-storage-server.md#block-mode) |
| `state-size` | `1Gi` | In block mode, size of the Filesystem PVC holding the API state | [Block mode](pod-storage-server.md#block-mode) |
| `dest-storage-class-prefix` | empty: the release name | Prefix of the generated StorageClasses `<prefix>-nfs` and `<prefix>-iscsi`; unique in the cluster | [Namespace layouts and multiple servers](pod-storage-server.md#namespace-layouts-and-multiple-servers), [Value formats](pod-storage-server.md#value-formats) |
| `tls.enabled` | `true` | Serve the API with mutual TLS only. `false` only with a driver installed with `apiTLS.enabled=false` | [Mutual TLS](pod-storage-server.md#mutual-tls), [Without TLS](pod-storage-server.md#without-tls) |
| `tls.issuer` | `lvmo-csi-api` | ClusterIssuer that signs the server's certificate: the driver's `apiTLS.issuer` | [Mutual TLS](pod-storage-server.md#mutual-tls) |
| `tls.clientCA` | empty | The driver CA certificate (PEM). Needed only to render offline (`helm template`, Argo CD); otherwise read from the driver's namespace | [Mutual TLS](pod-storage-server.md#mutual-tls) |
| `tls.existingSecret` | empty | A Secret of your own (`tls.crt`, `tls.key`: server certificate; `ca.crt`: the driver CA) instead of cert-manager | [Mutual TLS](pod-storage-server.md#mutual-tls) |
| `openshift` | `false` | `true` on OpenShift: the server and its pre-delete guard run as a service account allowed, by a Role of the release namespace, to use the built-in `privileged` SCC | [OpenShift](pod-storage-server.md#openshift) |
| `image.repository` | `michaelcourcy/lvmo-csi-storage-server` | Server image | [Images](#images) |
| `image.tag` | empty: the chart's appVersion | Server image tag | [Images](#images) |
| `image.pullPolicy` | `IfNotPresent` | Pull policy of the server image | [Images](#images) |
| `resources` | `{}` | Standard requests and limits of the server container | [Server resources](pod-storage-server.md#server-resources) |

## Images

Each chart's `image.repository`, `image.tag` and `image.pullPolicy` set its own
image. An empty tag uses the chart's appVersion, which is the release tag: the
charts of release `vX.Y.Z` use the images `michaelcourcy/lvmo-csi:vX.Y.Z` and
`michaelcourcy/lvmo-csi-storage-server:vX.Y.Z`, for Linux AMD64 and ARM64.
Keep the driver and server images of one release together: independent upgrades
do not guarantee compatibility between arbitrary versions.

To run a development build, push it to a registry of your own and set both the
repository and the tag. Release repositories receive only release builds.

The `sidecars.*` values are the upstream Kubernetes CSI sidecars the release was
tested with. Override them only to use a mirror, with the same versions.

## Kubelet directory

The node plugin mounts the kubelet's root directory, with bidirectional mount
propagation, to publish volumes into pods, and registers its socket under
`<kubeletRoot>/plugins/lvmo.csi.io` and `<kubeletRoot>/plugins_registry`. Most
distributions use `/var/lib/kubelet`. Set `kubeletRoot` when the kubelet uses
another directory, for example `/var/lib/k0s/kubelet` on k0s or
`/var/snap/microk8s/common/var/lib/kubelet` on MicroK8s. With a wrong value,
the node plugin is never registered and volumes cannot be mounted.
