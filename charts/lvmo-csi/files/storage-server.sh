#!/bin/bash
set -euo pipefail
root=/backing
vg=${VG:?}
if [[ ${1:-serve} == guard ]]; then
  timeout 10 bash -c 'echo > "/dev/tcp/$1/50061"' _ "$SERVER" || { echo 'Cannot inspect running server'; exit 1; }
  test -f "$root/state/state.json" || { echo "Cannot inspect backend state"; exit 1; }
  jq -e '(.volumes | type == "object") and (.snapshots | type == "object") and ([(.volumes // {}), (.snapshots // {}), (.deleting // {}), (.snapshot_deleting // {})] | all(length == 0))' "$root/state/state.json" || { echo 'Removal refused: volumes, snapshots or pending reclamation remain'; exit 1; }
  echo 'Backend state empty; removal permitted'
  exit
fi
clients=${NFS_CLIENTS:?}
mkdir -p "$root/state"
exec 9>"$root/server.lock"
flock -n 9 || { echo 'Backing store already owned'; exit 1; }
identity="$VG $SERVER"
if [[ -f "$root/identity" ]]; then
  [[ $(cat "$root/identity") == "$identity" ]] || { echo 'Backing store identity changed'; exit 1; }
else
  printf '%s\n' "$identity" >"$root/identity"
fi
modprobe loop
modprobe dm_thin_pool
modprobe nfsd
modprobe iscsi_target_mod
mountpoint -q /sys/kernel/config || mount -t configfs none /sys/kernel/config
mkdir -p /proc/fs/nfsd
mountpoint -q /proc/fs/nfsd || mount -t nfsd nfsd /proc/fs/nfsd
if [[ ! -e "$root/disk.img" ]]; then
  # Reserve 20% for filesystem, state and LVM overhead, without sparse allocation.
  case ${BACKING_SIZE:?} in
    *Gi) requested=$(( ${BACKING_SIZE%Gi} * 1073741824 ));;
    *Mi) requested=$(( ${BACKING_SIZE%Mi} * 1048576 ));;
    *G) requested=$(( ${BACKING_SIZE%G} * 1000000000 ));;
    *M) requested=$(( ${BACKING_SIZE%M} * 1000000 ));;
    *) echo 'Unsupported backing size'; exit 1;;
  esac
  fallocate -l "$((requested * 80 / 100 / 4194304 * 4194304))" "$root/disk.img"
fi
loop=$(losetup -j "$root/disk.img" -O NAME --noheadings | head -1)
if [[ -z "$loop" ]]; then loop=$(losetup --find --show "$root/disk.img"); fi
mkdir -p /etc/lvm
printf 'devices { filter = [ "a|^%s$|", "r|.*|" ] global_filter = [ "a|^%s$|", "r|.*|" ] } activation { udev_sync = 0 udev_rules = 0 }\n' "$loop" "$loop" >/etc/lvm/lvmlocal.conf
if ! pvs "$loop" >/dev/null 2>&1; then
  test ! -e "$root/initialized" || { echo 'Existing image lost LVM metadata'; exit 1; }
  pvcreate "$loop"
  vgcreate "$vg" "$loop"
fi
if ! lvs "$vg/lvmo-pool" >/dev/null 2>&1; then
  test ! -e "$root/initialized" || { echo "Existing pool missing"; exit 1; }
  lvcreate --yes --type thin-pool -l 90%FREE --poolmetadatasize 64M -n lvmo-pool "$vg"
fi
vgs "$vg"
lvs "$vg/lvmo-pool"
touch "$root/initialized"
vgchange -ay "$vg"
# This test mode reserves a dedicated node; no other NFS/LIO server may use it.
mkdir -p /run/dbus
dbus-daemon --system --fork
rpcbind || true
if [[ $(cat /proc/fs/nfsd/threads) != 0 ]]; then rpc.nfsd 0; fi
rpc.nfsd -N 3 8
# mountd reads /etc/mtab; minimal Ubuntu images do not provide it.
ln -sf /proc/mounts /etc/mtab
mkdir -p "$root/state/volumes" "$root/nfs-root/backing/state/volumes"
mount --bind "$root/state/volumes" "$root/state/volumes"
mount --make-shared "$root/state/volumes"
mount --rbind "$root/state/volumes" "$root/nfs-root/backing/state/volumes"
mkdir -p /etc/exports.d
printf '%s %s(ro,sync,fsid=0,crossmnt,no_subtree_check,root_squash)\n' "$root/nfs-root" "$clients" >/etc/exports.d/lvmo-pod-root.exports
exportfs -ra
rpc.mountd -F -N 3 &
mountd=$!
cleanup() {
  trap - TERM INT EXIT
  kill -TERM "$api" 2>/dev/null || true
  wait "$api" 2>/dev/null || true
  for f in /etc/exports.d/lvmo-*.exports; do
    [[ -f "$f" ]] || continue
    path=$(awk '{print $1}' "$f")
    exportfs -u "$clients:$path" || true
    rm -f "$f"
  done
  if [[ -f "$root/state/state.json" ]]; then
    while read -r iqn; do targetcli /iscsi delete "$iqn" || true; done < <(jq -r '.volumes // {} | .[] | .iqn // empty | select(length > 0)' "$root/state/state.json")
    while read -r id; do targetcli /backstores/block delete "$id" || true; done < <(jq -r '.volumes // {} | .[] | select(.protocol == "iscsi") | .id' "$root/state/state.json")
  fi
  rpc.nfsd 0
  kill -TERM "$mountd" 2>/dev/null || true
  wait "$mountd" 2>/dev/null || true
  umount -R "$root/nfs-root/backing/state/volumes" || true
  while read -r path; do umount "$path" || true; done < <(findmnt -rn -o TARGET | awk '/^\/backing\/state\/volumes\//')
  umount "$root/state/volumes" || true
  vgchange -an "$vg" && losetup -d "$loop"
}
trap cleanup EXIT
trap 'exit 0' TERM INT
/usr/local/bin/lvmo-csi -P=50061 --root="$root/state" --server="$SERVER" --nfs-clients="$clients" "$vg" 9>&- &
api=$!
wait -n "$api" "$mountd"
