#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
context=${KUBE_CONTEXT:-kind-lvmo-e2e}
mkdir -p "$root/.test/reports"
# Focus on the external driver's suites; unsupported capabilities are skipped
# by the upstream framework according to the driver definition.
[[ $(kubectl config current-context) == "$context" ]] || exit 1
e2e.test --kubeconfig="${KUBECONFIG:-/root/.kube/config}" \
 --storage.testdriver="$root/tests/external-nfs.yaml" \
 --ginkgo.focus='External.Storage.*lvmo.csi.io' \
 --ginkgo.skip='\[Disruptive\]|\[Serial\]|\[Slow\]|performance|stress' \
 --ginkgo.timeout=45m --report-dir="$root/.test/reports"
