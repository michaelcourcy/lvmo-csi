#!/usr/bin/env bash
set -euo pipefail
# Run on the disposable storage server after the suites have removed their PVCs.
for attempt in $(seq 1 60); do
 if jq -e '(.volumes|length)==0 and (.snapshots|length)==0 and (.owners|length)==0' /var/lib/lvmo/state.json >/dev/null; then
  remaining=$(lvs --noheadings -o lv_name --select lv_tags=lvmo lvmo-test1 lvmo-test2 | tr -d '[:space:]')
  [[ -z $remaining ]] || { echo "Orphaned test LVs: $remaining" >&2; exit 1; }
  if iscsiadm -m session 2>/dev/null | grep -q 'iqn.2026-09.io.lvmo:'; then echo 'Leaked iSCSI session' >&2; exit 1; fi
  if compgen -G '/sys/kernel/config/target/iscsi/iqn.2026-09.io.lvmo:*' >/dev/null; then echo 'Leaked iSCSI target' >&2; exit 1; fi
  echo 'Cleanup verified: no managed volumes, snapshots, node leases, or tagged LVs remain.'
  exit 0
 fi
 sleep 2
done
jq '{volumes:(.volumes|length),snapshots:(.snapshots|length),owners:(.owners|length),deleting:(.deleting|length)}' /var/lib/lvmo/state.json
exit 1
