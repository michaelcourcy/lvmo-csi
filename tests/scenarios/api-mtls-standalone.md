---
id: api-mtls-standalone
status: manual
groups: [basic]
requires: [kubernetes, storage-server, nfs-client, iscsi-client]
automation: none
---

# A standalone storage server serves its API with mutual TLS

> Passed on EKS (`eks-paris`) on 10 October 2026, with separate server and driver CAs.

## Purpose

Check the procedure of [storage-server.md](../../docs/storage-server.md#give-the-api-its-certificate):
a standalone server gets its certificate from the driver chart's
cert-manager ClusterIssuer, the driver reaches it over mutual TLS, other
clients are refused, and a renewed certificate is used without restarting the
API.

## Preconditions

- cert-manager is installed (`v1.21.2`), or the instance allows the agent to
  install it. Record whether the run installed it.
- A storage server reachable from every node, with a VG holding `lvmo-pool`,
  and a root shell on it. The run may replace its `lvmo-csi` binary with the
  commit under test and change its `lvmo-api` unit; record the original unit
  and binary version to restore them.
- The driver chart is not installed, or the instance's `driver-install` policy
  lets the agent reinstall it.
- `kubectl`, `jq` and `openssl` on the machine running the steps.

## Steps

1. Install `lvmo-csi` from `charts/lvmo-csi` in `lvmo-system`, with the
   development image and `apiTLS` enabled (the default), `--wait`.
2. Run `scripts/issue-api-server-cert.sh <name> <dir> <server private IP>`
   without `NAMESPACE`: it finds the driver's namespace on the ClusterIssuer.
   Check that Certificate `<name>-api-tls` is `Ready` in `lvmo-system`, that the
   certificate's extended key usage is `TLS Web Server Authentication` only,
   that it names the IP, and that the written `ca.crt` is the driver CA
   (`ca.crt` of Secret `lvmo-csi-api-driver-ca`), not the server CA.
3. Copy `tls.crt`, `tls.key` and `ca.crt` to `/etc/lvmo/tls` on the server
   (directory `0700`, files `0600`, owner root) without writing the key to
   any shared log or long-lived store. Add
   `--tls-cert=/etc/lvmo/tls/tls.crt --tls-key=/etc/lvmo/tls/tls.key --tls-client-ca=/etc/lvmo/tls/ca.crt`
   to the `lvmo-api` unit's `ExecStart`, `systemctl daemon-reload`, restart it.
   Its log contains `(mutual TLS: true)` and no `WARNING`.
4. Create StorageClasses `mtls-nfs` and `mtls-iscsi` with
   `endpoint: <server private IP>:50051` and the server's VG. Create a 256Mi
   PVC on each (NFS RWX, iSCSI RWO), mount each in a Pod, write a 16Mi random
   file and record its SHA-256, within 5 minutes.
5. From an unprivileged Pod in a new namespace, against the server's API:
   `grpcurl -plaintext` and `grpcurl -insecure` (no client certificate) calling
   `lvmo.v1.Storage/ListVolumes` both fail; `openssl s_client -connect <ip>:50051 -alpn h2`
   without a certificate shows the server certificate, then an alert. Record
   the outputs. Compare the alert with what
   [storage-server.md](../../docs/storage-server.md#verify-the-server) says.
6. **Renewal.** Record the `lvmo-api` main PID and the served certificate's
   serial. Delete Secret `<name>-api-tls` in `lvmo-system` so that cert-manager
   issues a new certificate, run the script of step 2 again, copy the files as
   in step 3, and do not restart `lvmo-api`. Restart the controller
   Deployment, so that the driver opens a new connection. Within 3 minutes the
   served serial (from `openssl s_client`) is the new one, the PID is unchanged,
   and a new 256Mi PVC on `mtls-iscsi` binds.

## Expected

- The driver provisions and mounts both protocols over mutual TLS (steps 3–4).
- Clients without the driver's certificate get no answer from the API (step 5).
- A renewed certificate is served without restarting the API (step 6).

## Evidence

- The certificate's usage, SAN and serial lines before and after renewal.
- The `lvmo-api` unit's `ExecStart`, its log lines, and its PID before and after.
- PVC and Pod status and file hashes.
- The outputs of step 5.

## Cleanup

- Delete the Pods, PVCs (wait for their PVs to be reclaimed) and the two
  StorageClasses, and the attacker namespace.
- Delete Certificate and Secret `<name>-api-tls`. Remove `/etc/lvmo/tls` and
  restore the original `lvmo-api` unit and binary, unless the instance file
  says to keep the TLS configuration.
- Uninstall what the run installed: the driver release (then Secret
  `lvmo-csi-api-server-ca` in `cert-manager`) and cert-manager.
