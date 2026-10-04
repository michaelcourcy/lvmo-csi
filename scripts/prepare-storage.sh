#!/usr/bin/env bash
set -euo pipefail
# Only for the dedicated disposable test VM. Never selects existing host disks.
mkdir -p /var/lib/lvmo-test-disks
for n in 1 2; do
  vg="lvmo-test${n}"
  if ! vgs "$vg" >/dev/null 2>&1; then
    disk="/var/lib/lvmo-test-disks/disk${n}.img"
    truncate -s 8G "$disk"
    loop=$(losetup --find --show "$disk")
    pvcreate "$loop"
    vgcreate "$vg" "$loop"
    lvcreate --yes --type thin-pool -L 6G --poolmetadatasize 128M -n lvmo-pool "$vg"
  fi
done
# LVM metadata lives in the images; only the loop attachments are lost on
# reboot. Re-attach them before the API reconciles its volumes.
cat > /etc/systemd/system/lvmo-loop.service <<'UNIT'
[Unit]
Description=Attach LVMO test loop disks
After=local-fs.target
Before=lvmo-api.service target.service
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'for d in /var/lib/lvmo-test-disks/*.img; do losetup -j "$d" | grep -q . || losetup --find "$d"; done'
ExecStartPost=/usr/bin/udevadm settle
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable lvmo-loop
# The server is also an initiator in direct tests. Never scan its exported
# iSCSI clones as candidate PVs (especially while testing failed logins).
cat > /etc/lvm/lvmlocal.conf <<'LVM'
devices { global_filter = [ "a|^/dev/loop[0-9]+$|", "r|.*|" ] }
LVM
