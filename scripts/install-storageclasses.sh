#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
context=${KUBE_CONTEXT:-kind-lvmo-e2e}
endpoint=${API_ENDPOINT:?Set API_ENDPOINT to the storage VM host:port}
kubectl --context "$context" create --dry-run=client -f "$root/tests/storageclasses.yaml" -o json |
 jq --arg endpoint "$endpoint" '.items |= map(if .kind == "StorageClass" then .parameters.endpoint = $endpoint else . end)' |
 kubectl --context "$context" apply -f -
