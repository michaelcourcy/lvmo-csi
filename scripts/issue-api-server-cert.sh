#!/usr/bin/env bash
set -euo pipefail
# Issue a certificate for a standalone storage server's API from the
# ClusterIssuer created by the lvmo-csi chart, and write to a directory, to
# copy to the server's /etc/lvmo/tls: tls.crt and tls.key, and ca.crt, the
# public certificate of the driver CA, the only CA whose client certificates
# the server will admit. Run it again after cert-manager renews the
# certificate: it only downloads the current one.
#
#   scripts/issue-api-server-cert.sh storage-a ./storage-a-tls storage-a.example.internal 10.0.0.10
#
# The Certificate is created in the driver's namespace, which the ClusterIssuer
# names; NAMESPACE overrides it.
#
# List every name or address a StorageClass uses in its endpoint: the driver
# checks the server certificate against the endpoint's host.
[[ $# -ge 3 ]] || { echo "usage: $0 <name> <output-directory> <dns-name-or-ip>..." >&2; exit 2; }
name=$1 out=$2
shift 2
issuer=${ISSUER:-lvmo-csi-api}
k() { kubectl ${KUBE_CONTEXT:+--context "$KUBE_CONTEXT"} "$@"; }
namespace=${NAMESPACE:-$(k get clusterissuer "$issuer" -o jsonpath='{.metadata.annotations.lvmo\.csi\.io/driver-namespace}')}
[[ -n $namespace ]] || { echo "ClusterIssuer $issuer names no driver namespace: install the lvmo-csi chart with apiTLS enabled, or set NAMESPACE" >&2; exit 1; }
jq -n --arg name "$name-api-tls" --arg namespace "$namespace" --arg issuer "$issuer" '$ARGS.positional as $hosts | {
  apiVersion: "cert-manager.io/v1", kind: "Certificate",
  metadata: {name: $name, namespace: $namespace},
  spec: {
    secretName: $name, commonName: $hosts[0],
    dnsNames: [$hosts[] | select(test("^[0-9.]+$|:") | not)],
    ipAddresses: [$hosts[] | select(test("^[0-9.]+$|:"))],
    duration: "8760h", renewBefore: "2160h",
    usages: ["digital signature", "server auth"],
    privateKey: {algorithm: "ECDSA", size: 256, rotationPolicy: "Always"},
    issuerRef: {kind: "ClusterIssuer", name: $issuer}
  }}' --args "$@" | k apply -f -
# Ready can still describe a deleted Secret for a moment: wait for the Secret too.
for _ in $(seq 1 60); do k -n "$namespace" get secret "$name-api-tls" >/dev/null 2>&1 && break; sleep 2; done
k -n "$namespace" wait --for=condition=Ready "certificate/$name-api-tls" --timeout=120s
umask 077
mkdir -p "$out"
for file in tls.crt tls.key; do
  k -n "$namespace" get secret "$name-api-tls" -o jsonpath="{.data.${file//./\\.}}" | base64 -d >"$out/$file"
done
k -n "$namespace" get secret "$issuer-driver-ca" -o jsonpath='{.data.ca\.crt}' | base64 -d >"$out/ca.crt"
echo "Wrote $out/tls.crt, tls.key and ca.crt (driver CA); the certificate expires $(openssl x509 -enddate -noout -in "$out/tls.crt" | cut -d= -f2)"
