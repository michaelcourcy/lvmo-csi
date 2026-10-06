# Build a storage server

This guide builds an lvmo storage server by hand on a dedicated Linux host, with real disks. It is written for the person who will run the server: it assumes you know LVM, NFS and iSCSI, but not lvmo.

The storage server is where volumes live. It runs one program, the lvmo API (`lvmo-csi`), which creates thin LVs in a pool you prepare and exports them over NFS or iSCSI. The Kubernetes side only needs the driver's Helm chart and StorageClasses that point to this server: see [Kubernetes / OpenShift](../README.md#kubernetes--openshift).

The test environments in [tests/environments/](../tests/environments/) build servers too, but on loop-file disks and with test-only settings. Do not copy them for a real server.

## 1. Size the server

lvmo delivers what the server's disks can do, shared fairly between volumes: in the [performance tests](performances-test.md), 20 workloads at once always reached the raw disks' limits, up to 630 MiB/s and 50 000 IOPS, without lvmo becoming the limit. So you size an lvmo server the way you would size any storage server. The disks set the ceiling, and the CPU, network and NFS threads must not set a lower one.

These hints come from fio runs on AWS instances standing in for datacenter servers, one run each. Treat them as orders of magnitude, and measure your own hardware (see [Verify the disks](#verify-the-disks)).

### Disks: they decide what workloads get

- **Workloads share the totals.** 20 busy volumes on disks that deliver 625 MiB/s get about 31 MiB/s each. Size the disks for the sum of what the busy volumes need at the same time, not for one volume.
- **Spread the pool over all disks.** LVM fills a VG's disks one after the other, so a plain VG over five disks performs like one disk until the first is full. Stripe the thin pool's data over the disks (`lvcreate -i <disks> -I 64k`, section 4), or build the VG on one RAID device. Five striped disks gave exactly five times one disk.
- **Plan for redundancy below LVM.** lvmo keeps one copy of each volume, on one server. A striped pool is lost if any disk fails. On a real server, put the VG on hardware RAID or `mdadm` RAID 10 (or RAID 6 for capacity, at the cost of random writes). Only plain disks and striping were tested.
- **NFS random writes cost more than iSCSI writes.** Each synchronous NFS write costs 1.5 to 3 disk writes on the server (the journal and metadata of its ext4 filesystem). On the same disks, NFS reached 33% to 65% of iSCSI's random write IOPS. Sequential throughput is the same for both protocols.
- **Latency is the disks' latency plus about 100 µs.** Under load it becomes the queueing of every volume on the shared disks: a single 4 KiB read took 0.4 ms with 20 workloads on one NVMe disk, and 6.7 ms on one 3000-IOPS disk.
- **Use e2fsprogs 1.47 or later, and keep the pool's zeroing on.** lvmo then formats ext4 volumes with `assume_storage_prezeroed`, so that their inode tables need no zeroing after the first mount. Without it, ext4's lazy initialization made each new iSCSI volume send about 29 MiB per GiB as 1 KiB writes (the iSCSI target emulates the zeroing that way): 20 new volumes took a 15 000-IOPS stripe's full capacity for about 4 minutes, and a 3000-IOPS disk's for about 25 minutes. With older e2fsprogs, or a pool created with `--zero n`, lvmo formats as before and this cost comes back. XFS volumes never had it.

### Capacity: retained snapshots keep the old data

A snapshot costs nothing when it is taken. Afterwards, every 64 KiB chunk that a volume rewrites gets a new chunk in the pool, while the snapshot keeps the old one. The pool's needs therefore depend on how many snapshots you keep and how much the data changes between them, not only on how much data the volumes hold:

**pool ≥ live data × (1 + retained snapshots × fraction of chunks rewritten between two snapshots)**

A single snapshot holds at most one full copy of its volume.

| Usage | Pool needed |
|---|---|
| No retained snapshots (Kasten exports, then retires the local snapshot) | about 1.1 × live data |
| 7 daily snapshots, 10% of the chunks rewritten each day | about 1.7 × |
| 7 daily snapshots of a database that rewrites itself (`VACUUM FULL`, reindex, restore over it) | up to 8 × |

Measured in the [Kasten export test](kasten-performance-test.md#thin-pool-space): a 58 GiB PostgreSQL volume with two retained snapshots peaked at 84 GiB, 1.45 times its data, after 20% of its rows were deleted. That deletion removed about 10 GB of rows but took 18 GiB in the pool: PostgreSQL also rewrote its index, its pages during `VACUUM`, and its recycled WAL files, and the pool counts whole chunks. Had the deleted rows been scattered across the table, nearly every chunk would have changed.

- **Start with twice the live data** when local snapshots are retained, then adjust from `data_percent` (section 8) once you know your workloads' real change rate.
- **Leave free extents in the VG** instead of giving everything to the pool, so that auto-extension (section 8) can grow the pool before it fills.
- **Keep local snapshot retention short when you export.** An exported copy lives in object storage and takes no space in the pool; a retained local snapshot does.

### CPU: the data path runs in the kernel

The iSCSI target (LIO) and the NFS server are kernel services, so their CPU cost shows up as system and softirq time, not as the lvmo process.

| Server | Load | CPU |
|---|---|---|
| 2 vCPU | 50 000 random read IOPS, iSCSI | 78% busy |
| 2 vCPU | 50 000 random read IOPS, NFS | 100% busy, the limit |
| 16 vCPU | 15 000 IOPS or 630 MiB/s, iSCSI | 2% system time |

Two cores are enough for a few thousand IOPS. Above about 20 000 IOPS, and earlier for NFS, give the server at least 4 cores, and more for NVMe disks.

### Memory: a read cache for NFS

The API itself needs little memory. The rest of the RAM becomes the page cache of the server's NFS filesystems: when an NFS volume's working set fits in it, reads come from memory (590 MiB/s and 50 000 IOPS from a server whose disk delivered 125 MB/s and 3000 IOPS). iSCSI reads always went to the disks. Size RAM for the NFS data you want served from memory. The tests used 8 and 16 GiB.

### Network: at least the disks' throughput

Everything the disks deliver crosses the server's network interface, in both directions.

| Disks deliver | Network needed |
|---|---|
| 125 MiB/s (one SATA SSD, one cloud volume) | More than 1 Gbit/s, which carries about 115 MiB/s |
| 300 MiB/s (one NVMe disk in the tests) | 10 Gbit/s |
| 630 MiB/s (five striped disks) | 10 Gbit/s; peaks of 1.1 GB/s were seen when 20 volumes were written at once |

Use a dedicated storage network if you can. A burstable cloud network can carry a short test but falls back to its baseline under sustained load.

### NFS server threads: more than Ubuntu's 8

With Ubuntu's default of 8 NFS server threads and disks of about 1 ms latency, NFS random writes stopped at 2 895 IOPS while the disks could do 15 000. With 64 threads they reached 8 626. A thread is busy for the whole synchronous write, so threads × (1 / time per request) caps the requests per second: 8 threads at about 2.7 ms give about 3 000. On NVMe, 8 threads were enough. Set 64 (section 6) unless your disks answer in tens of microseconds.

### iSCSI queue bound

The API bounds each iSCSI volume to 8 commands in flight (`--iscsi-queue-depth`) and sets a 2-minute SCSI command timeout on nodes (`--iscsi-command-timeout`). Without the bound, 20 iSCSI workloads on one overloaded disk hung instead of slowing down. Keep the defaults. Raise the depth only for fast disks shared by few volumes.

## 2. Prepare the host

Use a dedicated host for storage: Ubuntu 24.04 (the tested release), on amd64 or arm64. It needs a static address that every Kubernetes node can reach.

```sh
sudo apt-get update
sudo apt-get install -y lvm2 thin-provisioning-tools nfs-kernel-server targetcli-fb xfsprogs curl ca-certificates
```

Load the kernel modules now and at every boot:

```sh
printf '%s\n' dm_thin_pool target_core_mod iscsi_target_mod | sudo tee /etc/modules-load.d/lvmo.conf
sudo systemctl restart systemd-modules-load
sudo systemctl enable --now nfs-server
```

`open-iscsi` is not needed: the server is an iSCSI target, not an initiator.

## 3. Restrict which disks LVM scans

The pool's thin LVs hold whatever the workloads write, including, for raw block volumes such as VM disks, LVM physical volumes of their own. The server must never take those for its own PVs. Allow LVM to scan only the backing disks and the OS disk, using stable names:

```sh
ls -l /dev/disk/by-id/                  # find the data disks and the OS disk
sudo pvs                                # Ubuntu's default install puts the OS on LVM too
```

```sh
sudo tee /etc/lvm/lvmlocal.conf <<'LVM'
devices {
  global_filter = [
    "a|^/dev/disk/by-id/nvme-OS_DISK_ID-part3$|",
    "a|^/dev/disk/by-id/wwn-DATA_DISK_1$|",
    "a|^/dev/disk/by-id/wwn-DATA_DISK_2$|",
    "r|.*|"
  ]
}
LVM
sudo pvs    # every PV you expect, and nothing else
```

**Include the OS disk's PV if the OS is on LVM**, or the host will not boot. If `lvmconfig devices/use_devicesfile` prints `use_devicesfile=1`, LVM uses its devices file instead: add the disks with `lvmdevices --adddev` and check that `lvmdevices` lists nothing else. If you use RAID, allow the `md` or hardware RAID device, not its member disks.

## 4. Create the volume group and thin pool

Measure the disks before you create anything on them (see [Verify the disks](#verify-the-disks)): that is the ceiling lvmo can reach. The `pvcreate` below destroys what is on them.

```sh
sudo pvcreate /dev/disk/by-id/wwn-DATA_DISK_1 /dev/disk/by-id/wwn-DATA_DISK_2
sudo vgcreate lvmo-data /dev/disk/by-id/wwn-DATA_DISK_1 /dev/disk/by-id/wwn-DATA_DISK_2
```

Create the pool, named `lvmo-pool` (lvmo's default). Leave free space in the VG for auto-extension (section 8). With several disks and no RAID device, stripe the data over all of them: `-i` is the number of disks.

```sh
# One disk or one RAID device:
sudo lvcreate --type thin-pool -L 900G --chunksize 64k --poolmetadatasize 4G -n lvmo-pool lvmo-data
# Several disks, striped:
sudo lvcreate --type thin-pool -i 2 -I 64k -L 900G --chunksize 64k --poolmetadatasize 4G -n lvmo-pool lvmo-data
sudo lvs -o lv_name,lv_size,data_percent,metadata_percent,chunk_size,stripes lvmo-data
```

- **Chunk size** (`--chunksize`, 64 KiB to 1 GiB, a multiple of 64 KiB) is the pool's allocation unit and cannot change once the pool holds data. It is the same for every volume in the pool. Smaller chunks make the first write to a snapshotted chunk cheaper and make changed block tracking more precise, since changes are reported per chunk. Larger chunks need less metadata and suit large sequential workloads without snapshots. 64 KiB is a good default for lvmo's snapshot and backup use, and is what every test used. To offer another chunk size, use another VG with its own pool.
- **Metadata size** (`--poolmetadatasize`, at most about 16 GiB) grows with the number of mapped chunks, so it grows with smaller chunks, more volumes, and more snapshots that diverge from their source. Estimate it with `thin_metadata_size -b 64k -s 900g -m 1000 -u g` (chunk size, pool size, maximum number of volumes and snapshots), and leave a margin. You can extend it later with `lvextend --poolmetadatasize +1G lvmo-data/lvmo-pool`.
- **Keep zeroing on** (the default). With `--zero n`, a new volume can read data a deleted volume left in its chunks.

lvmo creates volumes in this pool but never creates VGs, sizes the pool, or repartitions disks.

## 5. Install the API

Use the same release as the Helm chart on the cluster:

```sh
LVMO_VERSION=v0.1.0-alpha.3
ARCH=$(dpkg --print-architecture)       # amd64 or arm64
cd "$(mktemp -d)"
curl -fLO "https://github.com/michaelcourcy/lvmo-csi/releases/download/$LVMO_VERSION/lvmo-csi-linux-$ARCH"
curl -fLO "https://github.com/michaelcourcy/lvmo-csi/releases/download/$LVMO_VERSION/SHA256SUMS"
awk -v f="lvmo-csi-linux-$ARCH" '$2 == f' SHA256SUMS > api.sha256
test -s api.sha256 && sha256sum --check api.sha256
sudo install -m 0755 "lvmo-csi-linux-$ARCH" /usr/local/bin/lvmo-csi
```

Only `lvmo-csi` runs on the server. `lvmo-driver` and `lvmo-metadata` run in the cluster, from the image.

Create the unit. Replace `10.0.0.10` with the address the nodes use to reach the server for NFS and iSCSI, and list every VG the server serves:

```sh
sudo tee /etc/systemd/system/lvmo-api.service <<'UNIT'
[Unit]
Description=lvmo storage API
Wants=network-online.target
After=network-online.target local-fs.target nfs-server.service
[Service]
ExecStart=/usr/local/bin/lvmo-csi --server=10.0.0.10 -P 50051 lvmo-data
Restart=on-failure
RestartSec=5
[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable --now lvmo-api
journalctl -u lvmo-api -n 20 --no-pager   # "lvmo API listening on [::]:50051"
```

At startup the API checks that each VG has a thin pool, then restores every volume: it activates the LVs, remounts and re-exports NFS volumes, and recreates iSCSI targets. No fstab entries, `/etc/exports` lines or `targetcli` configuration are needed, and you should not edit the ones it writes (`/etc/exports.d/lvmo-*.exports`, the LIO configuration). If the disks are not ready yet at boot, the API exits and systemd restarts it 5 seconds later.

The API's options:

| Option | Default | Use |
|---|---|---|
| `--server` | The address of the default route | The NFS and iSCSI address given to nodes. Set it explicitly with several interfaces, NAT, or a DNS name |
| `-P` | `50051` | The management API port; StorageClasses name it in `endpoint` |
| `--root` | `/var/lib/lvmo` | The state directory, and where NFS volumes are mounted. Only one API process may use it |
| `--pool` | `lvmo-pool` | The thin pool's name, the same in every VG |
| `--nfs-clients` | `*` | The NFS export client selector, for example `10.0.0.0/24` |
| `--nfs-insecure` | off | Allows NFS clients on unprivileged source ports. Needed only behind port forwarding, such as Lima |
| `--iscsi-queue-depth` | `8` | Commands in flight per iSCSI volume (section 1). `0` keeps the Linux and LIO defaults |
| `--iscsi-command-timeout` | `2m` | The SCSI command timeout nodes set on lvmo disks |

A changed iSCSI option applies to a volume the next time it is attached to a node.

## 6. Tune NFS

Raise the NFS server threads (see section 1):

```sh
sudo nfsconf --set nfsd threads 64
sudo systemctl restart nfs-server
cat /proc/fs/nfsd/threads    # 64
```

Volumes are mounted on the server, so enable weekly trimming. Files deleted in NFS volumes then give their chunks back to the pool:

```sh
sudo systemctl enable --now fstrim.timer
```

## 7. Open the network to the nodes only

Nodes use three TCP ports: 50051 (management API), 2049 (NFS 4.1) and 3260 (iSCSI). The management API and iSCSI targets have no authentication, and NFS exports use `no_root_squash`. **Open them to the cluster nodes' network only, never publicly.** With `ufw`, for nodes in `10.0.0.0/24`:

```sh
sudo ufw allow OpenSSH
for port in 50051 2049 3260; do sudo ufw allow from 10.0.0.0/24 to any port $port proto tcp; done
sudo ufw enable
```

iSCSI access is also limited per volume: once a volume is attached, only its node's initiator may log in.

## 8. Monitor and protect the pool

lvmo never sizes, extends, or watches the pool, and it accepts a PVC even when the pool is nearly full. A volume's size is a limit, not a reservation, so the sum of volume sizes may exceed the pool. Kubernetes cannot see the pool's state either. Keeping the pool healthy is your job.

- **Auto-extend**: leave free extents in the VG, and in the `activation` section of `/etc/lvm/lvm.conf` set `thin_pool_autoextend_threshold = 80` and `thin_pool_autoextend_percent = 20`. `dmeventd` (`lvm2-monitor.service`) then extends the data and metadata LVs when either passes the threshold. The default threshold, `100`, disables it.
- **Alert** on both `data_percent` and `metadata_percent`, from `lvs -o lv_name,lv_size,data_percent,metadata_percent lvmo-data/lvmo-pool`, or on the `dmeventd` warnings in the system log, which start at 80%. When data is full, writes in every pod using the pool wait 60 seconds, then fail. When metadata is full, the pool can switch to read-only and need `lvconvert --repair`, which is worse.
- **Give space back**: the iSCSI targets advertise UNMAP, so discards on the nodes (`fstrim`, `blkdiscard`, or a filesystem mounted with `discard`) free chunks. NFS volumes are trimmed on the server by `fstrim.timer` (section 6).
- **Watch the disks**: `iostat -x 10` shows when the disks are 100% busy. That is the expected limit, and then every volume slows down evenly.

## 9. Verify

### Verify the disks

Measure what the disks deliver, before section 4 or on a scratch LV, so that you know what lvmo should reach. For example, with `fio` on a striped scratch LV (this overwrites it):

```sh
sudo apt-get install -y fio
sudo lvcreate -i 2 -I 64k -L 20G -n fio-scratch lvmo-data
for rw in "write --bs=1M --iodepth=16" "randwrite --bs=4k --iodepth=32" "randread --bs=4k --iodepth=32"; do
  sudo fio --name=raw --filename=/dev/lvmo-data/fio-scratch --direct=1 --ioengine=libaio --numjobs=4 --runtime=60 --time_based --group_reporting --rw=$rw
done
sudo lvremove -y lvmo-data/fio-scratch
```

### Verify the server

On the server:

```sh
systemctl is-active lvmo-api nfs-server        # active, active
sudo ss -ltn '( sport = :50051 or sport = :2049 or sport = :3260 )'
sudo lvs lvmo-data/lvmo-pool                    # a thin pool
```

Port 3260 listens only once a first iSCSI volume exists.

From every Kubernetes node (for example in `kubectl debug node/<name> -it --image=busybox`):

```sh
nc -zv 10.0.0.10 50051
nc -zv 10.0.0.10 2049
```

Then install the chart and a StorageClass with `endpoint: <server>:50051` and `vg: lvmo-data`, and create a test PVC for each protocol.

## 10. Operate

### Back up

The server holds the only copy of each volume, with no replication. Protect volumes with a backup product, Kasten for example, which can use lvmo snapshots.

To rebuild the server itself, keep:

- `/var/lib/lvmo`: the API's state, which lists every volume, snapshot and attachment. Keep it together with the disks: a server with the disks but without this directory does not know its volumes.
- LVM's metadata, which LVM keeps in `/etc/lvm/backup` and `/etc/lvm/archive` and on the disks themselves.
- `/etc/systemd/system/lvmo-api.service`, `/etc/lvm/lvmlocal.conf`, `/etc/lvm/lvm.conf`, and `/etc/nfs.conf`.

The exports and iSCSI targets are recreated by the API at startup, and need no backup.

### Upgrade

```sh
# Download and check the new binary as in section 5, then:
sudo systemctl stop lvmo-api
sudo install -m 0755 "lvmo-csi-linux-$ARCH" /usr/local/bin/lvmo-csi
sudo systemctl start lvmo-api
```

NFS and iSCSI are served by the kernel, so volumes stay available while the API is stopped. Only creating, deleting, attaching and snapshotting volumes wait. Upgrade the server and the Helm chart to the same release.

### Add disks to the existing pool

The simplest way to add capacity is to add PVs to the existing VG and grow its pool. It works online: volumes stay in use, and the API needs no restart or configuration change, because it uses whatever capacity the pool has.

```sh
# Allow the new disk in the LVM filter first (section 3), then:
sudo pvcreate /dev/disk/by-id/wwn-DATA_DISK_3
sudo vgextend lvmo-data /dev/disk/by-id/wwn-DATA_DISK_3
sudo lvextend -L +900G lvmo-data/lvmo-pool
sudo lvs -o lv_name,lv_size,data_percent,metadata_percent,stripes lvmo-data
```

Without `lvextend`, the new space stays free in the VG, where auto-extension (section 8) can use it.

How the new space performs depends on how the pool was built:

- **One RAID device**: grow the array, or add another device of the same RAID type, so that the new space has the same redundancy.
- **A striped pool**: `lvextend` keeps the pool's stripe count, so it needs free space on as many PVs as there are stripes. For a pool striped over 2 disks, add 2 disks. With fewer, `lvextend` fails. Forcing it with `-i 1` adds a linear segment on the new disk: chunks allocated there get one disk's performance, not the stripe's, and workloads on them see the difference.
- **The metadata does not grow with the data.** Check `metadata_percent` after growing the data, and extend it with `lvextend --poolmetadatasize +1G lvmo-data/lvmo-pool` if the new size needs more (estimate with `thin_metadata_size`, section 4).

### Add a volume group

To add capacity with a different chunk size, separate disks, or another class of disks: allow the new disks in the LVM filter (section 3), create the VG and its `lvmo-pool` (section 4), add the VG to `ExecStart` in the unit, then run `systemctl daemon-reload` and `systemctl restart lvmo-api`. StorageClasses select it with the `vg` parameter.

### Several servers

One driver installation can use several storage servers: build each one with this guide, and give each its own StorageClasses, with its own `endpoint`. Use stable DNS names in `endpoint`. Existing volumes keep the endpoint they were created with, even if a StorageClass changes.
