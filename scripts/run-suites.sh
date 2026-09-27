#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$root/.test/reports"
(( $# )) || set -- sanity external snapshots metadata
for suite in "$@"; do
 case $suite in
 sanity)
  for protocol in nfs iscsi; do
   CSI_ENDPOINT=unix:///tmp/lvmo-csi.sock PROTOCOL="$protocol" "$root/bin/sanity.test" -test.v -test.timeout=30m | tee "$root/.test/reports/sanity-$protocol.log"
  done;;
 external) bash "$root/scripts/test-external.sh";;
 snapshots)
  for sc in lvmo-nfs lvmo-iscsi; do STORAGE_CLASS="$sc" bash "$root/scripts/test-snapshots.sh"; done;;
 metadata)
  CSI_ENDPOINT=unix:///tmp/lvmo-csi.sock "$root/bin/integration.test" -test.v -test.timeout=15m
  bash "$root/scripts/enable-metadata.sh"
  for mode in nfs iscsi-filesystem iscsi-block; do TEST_SOURCE_MODE="$mode" bash "$root/scripts/test-metadata.sh"; done
  bash "$root/scripts/test-routing.sh";;
 *) echo "Unknown suite: $suite" >&2; exit 2;;
 esac
done

bash "$root/scripts/check-cleanup.sh"
