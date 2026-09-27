#!/usr/bin/env bash
set -euo pipefail
# Run on the disposable storage server after the suites have removed their PVCs.
for attempt in $(seq 1 60); do
 if jq -e '(.volumes|length)==0 and (.snapshots|length)==0 and (.owners|length)==0' /var/lib/lvmo/state.json >/dev/null; then
  remaining=$(lvs --noheadings -o lv_name --select lv_tags=lvmo lvmo-test1 lvmo-test2 | tr -d '[:space:]')
  [[ -z $remaining ]] || { echo "Orphaned test LVs: $remaining" >&2; exit 1; }
  echo 'Cleanup verified: no managed volumes, snapshots, node leases, or tagged LVs remain.'
  exit 0
 fi
 sleep 2
done
jq '{volumes:(.volumes|length),snapshots:(.snapshots|length),owners:(.owners|length),deleting:(.deleting|length)}' /var/lib/lvmo/state.json
exit 1
