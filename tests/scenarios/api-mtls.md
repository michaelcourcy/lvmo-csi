---
id: api-mtls
status: manual
groups: [basic]
requires: [kubernetes, nfs-client, iscsi-client, multi-node]
automation: none
---

# Only the driver can call a pod storage server's API

> Passed on EKS (`eks-paris`) on 10 October 2026, with separate server and driver CAs.

## Purpose

The pod storage server publishes its management API on a ClusterIP Service,
so every pod in the cluster can reach TCP 50051, and the API can attach, read
and delete any volume. A compromised image in any pod must not be able to use
it. The API therefore requires mutual TLS: the driver presents a client
certificate from the driver CA, which only the driver's namespace can use, and
checks the server's certificate, from the server CA's ClusterIssuer, against
the StorageClass endpoint. This scenario checks that the driver still works,
that every other client is refused, including one holding a certificate
obtained from the ClusterIssuer in another namespace, and that renewed
certificates are picked up without restarting the driver
([pod-storage-server.md](../../docs/pod-storage-server.md#mutual-tls)).

## Preconditions

- cert-manager is installed (`v1.21.2`, chart `oci://quay.io/jetstack/charts/cert-manager`
  with `crds.enabled=true`), or the instance allows the agent to install it.
  Record its version and whether the run installed it.
- Snapshot CRDs and controller are installed.
- A non-lvmo Filesystem StorageClass can supply a 5Gi RWO source PVC, as for
  [pod-storage-server](pod-storage-server.md).
- Development driver and server images built from the commit under test are in
  the environment's permitted registry.
- Two schedulable nodes: the consumers run on a node other than the server's.
- No ClusterIssuer `lvmo-csi-api` exists yet, and no release or class name
  below collides.

## Steps

1. **Offline.** `bash scripts/test-pod-storage-chart.sh` and
   `go test ./internal/apitls/ ./internal/routing/` pass.
2. **Driver.** Install release `lvmo` from `charts/lvmo-csi` in namespace
   `lvmo-tls` with the development image and defaults otherwise (`apiTLS`
   enabled), `--wait --timeout 5m`. Check, with a 2-minute timeout each:
   - ClusterIssuer `lvmo-csi-api` is `Ready`, with annotation
     `lvmo.csi.io/driver-namespace: lvmo-tls`; Certificate
     `lvmo-csi-api-server-ca` in `cert-manager` is `Ready` with `isCA: true`.
   - In `lvmo-tls`: Certificate `lvmo-csi-api-driver-ca` (`isCA: true`), Issuer
     `lvmo-csi-api-driver` and Certificate `lvmo-csi-api-server-trust` are
     `Ready`. The `ca.crt` of `lvmo-csi-api-server-trust` is the server CA.
   - Certificate `lvmo-csi-api-client` in `lvmo-tls` is `Ready` and refers to
     Issuer `lvmo-csi-api-driver`. Decode its Secret's `tls.crt` with
     `openssl x509 -noout -text`: the issuer is the driver CA and the extended
     key usage is `TLS Web Client Authentication` only.
   - The controller and every node pod are `Running`, mount `/api-tls`, and
     their `driver` container log has no `WARNING` about TLS.
3. **Server.** Install release `tls-a` from `charts/lvmo-csi-storage-server` in
   namespace `lvmo-tls-storage` with the development image,
   `source-storage-class=<class>`, `size=5Gi`, `--wait --timeout 10m`.
   - Certificate `tls-a-storage-api-tls` is `Ready`; its certificate's extended
     key usage is `TLS Web Server Authentication` only, and its DNS names
     include `tls-a-storage.lvmo-tls-storage.svc`.
   - ConfigMap `tls-a-storage-api-client-ca` holds the driver CA's certificate
     (same as `ca.crt` in Secret `lvmo-csi-api-driver-ca`), and the file
     `/api-tls/ca.crt` in the server matches it.
   - The server log contains `lvmo API listening on [::]:50051 (mutual TLS: true)`.
   - A server install naming a ClusterIssuer that does not exist is refused:
     `helm install tls-b charts/lvmo-csi-storage-server -n lvmo-tls-storage --set source-storage-class=<class> --set tls.issuer=missing-issuer --dry-run=server`
     and check it fails with `ClusterIssuer missing-issuer not found`.
4. **Driver traffic works.** In namespace `lvmo-tls-consumers`, create a 256Mi
   NFS RWX PVC on `tls-a-nfs` and a 256Mi iSCSI RWO PVC on `tls-a-iscsi`, each
   mounted by a Pod on a node other than the server's. Within 5 minutes write a
   16Mi random file in each and record its SHA-256. Snapshot the iSCSI PVC with
   the driver's `lvmo-snapshots` class, restore it to a new PVC on
   `tls-a-iscsi`, mount it and compare the hash.
5. **Other clients are refused.** In a new namespace `lvmo-tls-attacker`, run
   an unprivileged Pod with the default service account, `grpcurl` and
   `openssl`, and `api/v1/storage.proto` from a ConfigMap. Against
   `tls-a-storage.lvmo-tls-storage.svc:50051`, call the read-only
   `lvmo.v1.Storage/ListVolumes` and record each output:
   1. `grpcurl -plaintext`: fails, no volume listed.
   2. TLS without a client certificate (`grpcurl -insecure`): fails, with a TLS
      alert such as `certificate required` or `bad certificate`.
   3. A self-signed client certificate with `clientAuth` usage, made with
      `openssl` in the Pod: fails.
   4. The server's own certificate and key as a client certificate (copy
      Secret `tls-a-storage-api-tls` into the attacker namespace, to model a
      stolen server certificate): fails.
   5. A certificate minted from the ClusterIssuer in the attacker namespace:
      create Certificate `minted` there with `issuerRef` ClusterIssuer
      `lvmo-csi-api`, `commonName: lvmo-csi-driver` and usages
      `digital signature, client auth`. It becomes `Ready`; with its key as the
      client certificate, the call fails.
   6. Control: the driver's certificate (copy Secret `lvmo-csi-api-client`):
      succeeds and lists the volumes of step 4. Delete the copied Secrets and
      Certificate `minted` right after.
   Also run `openssl s_client -connect <service>:50051 -alpn h2` without a
   certificate and record the alert the server sends.
6. **The driver checks the server's name.** Create StorageClass `tls-a-by-ip`
   with the parameters of `tls-a-nfs` but `endpoint: <Service ClusterIP>:50051`.
   A 256Mi PVC on it stays `Pending` for 2 minutes, with a provisioning event
   that mentions the certificate. Delete the PVC and the class.
7. **Renewal without restarting the driver.** Delete the consumer Pods (keep
   the PVCs). Record the driver pods' UIDs and restart counts and the serial of
   both certificates. Delete Secrets `lvmo-csi-api-client` and
   `tls-a-storage-api-tls`; cert-manager issues new ones. Within 3 minutes:
   - Both Certificates are `Ready` again with new serials.
   - The files mounted in a driver pod (`sha256sum /api-tls/tls.crt`) and in
     the server pod match the new Secrets.
   - Without restarting the server, `openssl s_client -showcerts` from the
     attacker Pod shows the new server serial.
   - Delete the server Pod (`kubectl delete pod -l app=tls-a-storage`), so that
     the driver must open new connections. Within 5 minutes, a new 256Mi PVC on
     `tls-a-iscsi` binds, and new Pods mount the step 4 PVCs on the consumer
     node with unchanged hashes. The driver pods' UIDs and restart counts are
     unchanged.

## Expected

- Steps 1–4 pass: NFS and iSCSI provisioning, mounting, snapshot and restore
  work over mutual TLS, and no lvmo component logs a TLS warning.
- In step 5, every call except the control fails, and none returns volume
  data, including with the certificate minted from the ClusterIssuer. The
  control succeeds.
- In step 6, the driver refuses a server certificate that does not name the
  endpoint's host.
- In step 7, renewed certificates are used by the server and the driver with
  no driver restart, and data written before the renewal is intact.

## Evidence

- cert-manager version, chart values, `kubectl get clusterissuer,certificate -A`.
- The `openssl x509 -text` usage and SAN lines for the client and server
  certificates, before and after renewal, with serials.
- The server log line and the driver logs' first 20 lines.
- PVC, Pod and snapshot status, file hashes, and the iSCSI/NFS mounts.
- The full output of each step 5 call and of `openssl s_client`.
- The step 6 provisioning event.
- Driver pod UIDs and restart counts before and after step 7.

## Cleanup

- Delete namespaces `lvmo-tls-attacker` and `lvmo-tls-consumers`, and wait for
  their PVs and VolumeSnapshotContents to be reclaimed.
- Uninstall `tls-a` (its pre-delete guard must pass), delete its retained
  source PVC and namespace `lvmo-tls-storage`.
- Uninstall `lvmo` and delete namespace `lvmo-tls`. Delete Secret
  `lvmo-csi-api-server-ca` in `cert-manager` (cert-manager does not delete it
  with its Certificate).
- Uninstall cert-manager and its CRDs only if this run installed it.

## Observations

- Whether a TLS 1.3 client sees the refusal at the handshake or at the first
  call, as reported by `grpcurl` and `openssl`.
- How long the kubelet took to update the mounted Secrets in step 7.

## Design notes

- Two CAs. The server CA signs servers through ClusterIssuer `lvmo-csi-api`, so
  that a server can be installed in any namespace. The driver CA signs only the
  driver, through an Issuer of the driver's namespace. Each side trusts only
  the other side's CA, and usages separate clients from servers.
- The NFS and iSCSI data ports remain unauthenticated; they are out of scope.
