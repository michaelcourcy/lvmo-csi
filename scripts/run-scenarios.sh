#!/usr/bin/env bash
set -euo pipefail
# Runs automated scenarios from tests/scenarios by id or group. Legacy suite
# names expand to scenario ids. Usage:
#   run-scenarios.sh [id|group|suite ...]            run them, then cleanup-audit
#   run-scenarios.sh --resolve [id|group|suite ...]  only print the ids
# CLEANUP_AUDIT=false skips the trailing audit (used when dispatching one id).
root=$(cd "$(dirname "$0")/.." && pwd)
dir="$root/tests/scenarios"
context=${KUBE_CONTEXT:-kind-lvmo-e2e}

# Prints one front-matter field of a scenario, brackets removed.
field() { awk -v k="$2" '/^---$/{n++; next} n==1 && $1==k":" {sub(/^[^:]*: */,""); sub(/ *#.*/,""); gsub(/[][]/,""); print; exit}' "$dir/$1.md"; }
ids() { for f in "$dir"/*.md; do f=${f##*/}; [[ $f == _* ]] || echo "${f%.md}"; done; }
in_list() { local item=$1; shift; for x in "$@"; do [[ $x == "$item" ]] && return 0; done; return 1; }

resolve() {
 local out=() arg id expanded groups
 # Default: the original suites in their usual order, then any other automated scenario.
 (( $# )) || set -- sanity external snapshots metadata $(for id in $(ids); do if [[ $(field "$id" status) == automated ]]; then echo "$id"; fi; done)
 for arg in "$@"; do
  case $arg in
  sanity) expanded=csi-sanity;;
  external) expanded=kubernetes-external-nfs;;
  snapshots) expanded=snapshot-restore-filesystem;;
  metadata) expanded="cbt-reconstruction-driver failed-create-recovery cbt-metadata-endpoint backend-routing";;
  *)
   if [[ -f $dir/$arg.md && $arg != _* ]]; then expanded=$arg
   else
    expanded=$(for id in $(ids); do groups=$(field "$id" groups | tr ',' ' '); if in_list "$arg" $groups; then echo "$id"; fi; done)
    [[ -n $expanded ]] || { echo "Unknown scenario, group or suite: $arg" >&2; return 2; }
   fi;;
  esac
  for id in $expanded; do
   if [[ $(field "$id" status) != automated ]]; then echo "Skipping $id: status $(field "$id" status), not automated" >&2; continue; fi
   in_list "$id" "${out[@]+"${out[@]}"}" || out+=("$id")
  done
 done
 if [[ ${#out[@]} -eq 0 ]]; then echo "No automated scenario selected" >&2; return 2; fi
 # The audit expects every other scenario to have cleaned up, so it runs last.
 for id in "${out[@]+"${out[@]}"}"; do [[ $id == cleanup-audit ]] || echo "$id"; done
 if [[ ${CLEANUP_AUDIT:-true} == true ]] || in_list cleanup-audit "${out[@]+"${out[@]}"}"; then echo cleanup-audit; fi
}

# Enabling the metadata sidecar regenerates its certificate; do it only once.
ensure_metadata() {
 helm get values lvmo --kube-context "$context" -n lvmo-system -o json | jq -e '.metadata.enabled == true' >/dev/null 2>&1 ||
  bash "$root/scripts/enable-metadata.sh"
}

run() {
 case $1 in
 csi-sanity)
  for protocol in nfs iscsi; do
   CSI_ENDPOINT=unix:///tmp/lvmo-csi.sock PROTOCOL="$protocol" "$root/bin/sanity.test" -test.v -test.timeout=30m | tee "$root/.test/reports/sanity-$protocol.log"
  done;;
 kubernetes-external-nfs)
  if [[ ${OPENSHIFT:-false} == true ]]; then bash "$root/scripts/test-external-pod.sh"; else bash "$root/scripts/test-external.sh"; fi;;
 snapshot-restore-filesystem)
  for sc in lvmo-nfs lvmo-iscsi; do STORAGE_CLASS="$sc" bash "$root/scripts/test-snapshots.sh"; done;;
 cbt-reconstruction-driver)
  CSI_ENDPOINT=unix:///tmp/lvmo-csi.sock "$root/bin/integration.test" -test.v -test.timeout=15m -test.run '^TestBackupReconstruction$';;
 failed-create-recovery)
  CSI_ENDPOINT=unix:///tmp/lvmo-csi.sock "$root/bin/integration.test" -test.v -test.timeout=15m -test.run '^TestFailedCreateRecovery$';;
 cbt-metadata-endpoint)
  ensure_metadata
  for mode in nfs iscsi-filesystem iscsi-block; do TEST_SOURCE_MODE="$mode" bash "$root/scripts/test-metadata.sh"; done;;
 backend-routing)
  ensure_metadata
  bash "$root/scripts/test-routing.sh";;
 cleanup-audit) bash "$root/scripts/check-cleanup.sh";;
 *) echo "No automation for scenario $1" >&2; return 2;;
 esac
}

if [[ ${1:-} == --resolve ]]; then shift; resolve "$@"; exit; fi
selected=$(resolve "$@")
mkdir -p "$root/.test/reports"
for id in $selected; do
 echo "=== scenario $id"
 run "$id"
done
