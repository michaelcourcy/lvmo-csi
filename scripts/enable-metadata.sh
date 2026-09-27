#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
context=${KUBE_CONTEXT:-kind-lvmo-e2e}
namespace=${DRIVER_NAMESPACE:-lvmo-system}
k() { kubectl --context "$context" "$@"; }
k apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshot-metadata/v1.0.0/client/config/crd/cbt.storage.k8s.io_snapshotmetadataservices.yaml
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
openssl req -x509 -newkey rsa:2048 -nodes -days 7 -keyout "$work/tls.key" -out "$work/tls.crt" \
 -subj "/CN=lvmo-metadata.${namespace}.svc" -addext "subjectAltName=DNS:lvmo-metadata.${namespace}.svc" >/dev/null 2>&1
k -n "$namespace" create secret tls lvmo-metadata-tls --cert="$work/tls.crt" --key="$work/tls.key" --dry-run=client -o yaml | k apply -f -
base64 < "$work/tls.crt" | tr -d '\n' > "$work/ca.b64"
helm upgrade lvmo "$root/charts/lvmo-csi" --kube-context "$context" -n "$namespace" \
 --reuse-values --set metadata.enabled=true --set-file metadata.caCert="$work/ca.b64" --wait --timeout 5m
