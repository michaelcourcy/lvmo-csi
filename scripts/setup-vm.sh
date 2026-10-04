#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq lvm2 thin-provisioning-tools nfs-kernel-server targetcli-fb open-iscsi xfsprogs jq gettext-base curl ca-certificates
modprobe dm_thin_pool
modprobe target_core_mod
modprobe iscsi_target_mod
systemctl enable --now nfs-server iscsid
root=$(cd "$(dirname "$0")/.." && pwd)
bash "$root/scripts/prepare-storage.sh"
if [[ -n ${RELEASE_VERSION:-} ]]; then
 bash "$root/scripts/install-release.sh" "$RELEASE_VERSION"
else
 for cmd in lvmo-csi lvmo-driver lvmo-metadata; do install -m 0755 "$root/bin/$cmd" "/usr/local/bin/$cmd"; done
fi
server=${STORAGE_SERVER:-$(ip -4 route get 1.1.1.1 | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')}
cat > /etc/systemd/system/lvmo-api.service <<UNIT
[Unit]
Description=LVMO storage management
Requires=lvmo-loop.service
After=network-online.target nfs-server.service lvmo-loop.service
[Service]
ExecStart=/usr/local/bin/lvmo-csi --server=$server lvmo-test1 lvmo-test2
Restart=on-failure
[Install]
WantedBy=multi-user.target
UNIT
cat > /etc/systemd/system/lvmo-driver.service <<'UNIT'
[Unit]
Description=LVMO direct CSI test endpoint
After=lvmo-api.service
[Service]
ExecStart=/usr/local/bin/lvmo-driver --endpoint=unix:///tmp/lvmo-csi.sock --api-endpoint=127.0.0.1:50051 --node-id=test-server
Restart=on-failure
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now lvmo-api lvmo-driver

for attempt in $(seq 1 60); do
 if [[ -S /tmp/lvmo-csi.sock ]] && (echo >/dev/tcp/127.0.0.1/50051) 2>/dev/null; then exit 0; fi
 sleep 1
done
journalctl -u lvmo-api -u lvmo-driver --no-pager -n 40
exit 1
