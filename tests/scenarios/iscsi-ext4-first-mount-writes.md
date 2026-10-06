---
id: iscsi-ext4-first-mount-writes
status: manual
groups: []
requires: [kubernetes, storage-server, iscsi-client]
automation: none
---

# A new ext4 iSCSI volume does not flood the disk with small writes after its first mount

## Purpose

`mkfs.ext4` leaves a new filesystem's inode tables to the `ext4lazyinit` kernel thread, which zeroes them after the first mount. On an lvmo iSCSI volume the node sends that zeroing as WRITE SAME commands, and LIO emulates them as 1 KiB writes because a thin LV has no write-zeroes support. The NVMe performance run measured about 290 MiB per new volume in 1 KiB writes, 5.35 million writes for 20 volumes ([performances-test.md](../../docs/performances-test.md#what-the-nvme-run-revealed-about-new-iscsi-volumes)). This took the disk's IOPS away from every other volume for minutes.

A new thin LV reads as zeros, and a pool with zeroing on (the default, required by [storage-server.md](../../docs/storage-server.md)) zeroes every chunk it provisions. The backend therefore formats ext4 with `-E assume_storage_prezeroed=1`, which marks the inode tables as zeroed without writing them. This scenario checks that the writes are gone and that the filesystem is sound.

## Preconditions

- StorageClass `lvmo-iscsi` (ext4, the default filesystem) on a VG whose thin pool has zeroing on.
- e2fsprogs 1.47 or later on the storage server (`mke2fs -V`).
- A root shell on the storage server.

## Steps

1. On the storage server, record `mke2fs -V` and `lvs --noheadings --binary -o zero <vg>/lvmo-pool` for the StorageClass's VG.
2. Create namespace `lvmo-lazyinit` with a 4Gi RWO PVC `data` on `lvmo-iscsi`, and wait until it is Bound. Do not create a pod yet.
3. On the storage server, find the volume's LV (`lvs -o lv_name,lv_tags <vg>`, the newest `v-` LV) and its device-mapper name (`dmsetup info -c -o name,blkdevname`). Count the block groups and the groups flagged `ITABLE_ZEROED` in `dumpe2fs <device> 2>/dev/null` (lines matching `^Group [0-9]`; `^Group ` alone also matches the header line `Group descriptor size`).
4. Record the LV's write counters: fields 5 (writes completed) and 7 (sectors written) of `/sys/block/<dm-N>/stat`.
5. Create a pod `writer` (`busybox`, `sleep 3600`) mounting `data` on `/data`, and wait until it is Ready (timeout 3 minutes). Do not write to the volume.
6. Wait 180 seconds after the pod is Ready, then record the LV's write counters again. On the pod's node, record whether an `ext4lazyinit` thread is running (`ps -e | grep ext4lazyinit`, or `kubectl debug node/...`).
7. Write 100 MiB to `/data/file` with `dd if=/dev/urandom bs=1M count=100 conv=fsync`, and record its SHA-256.
8. Delete the pod and wait until the volume is unstaged (the target has no session for it). On the storage server, run `blockdev --flushbufs` on the LV's device, then `e2fsck -fn`. The flush matters: the target writes past the server's page cache, so blocks that step 3 read are stale there, and `e2fsck` would report false errors such as an entry referencing an inode in the "unused inodes area".

## Expected

- Step 1: the pool's zeroing flag is `1`, and e2fsprogs is 1.47 or later.
- Step 3: every block group is flagged `ITABLE_ZEROED`.
- Step 6: between steps 4 and 6, the LV received fewer than 5 000 writes and less than 32 MiB (65 536 sectors). Before the change, a new volume received about 29 MiB per GiB in 1 KiB writes: about 120 MiB and 120 000 writes for 4 GiB.
- Step 8: `e2fsck -fn` reports no error (exit code 0).

## Validation

Passed on `eks-paris` on 2026-10-06 (e2fsprogs 1.47.0, loop pool `lvmo-test1`): 32 of 32 groups `ITABLE_ZEROED`; 6 writes and 4 KiB in the 190 s after the first mount; no `ext4lazyinit` thread on the node; `e2fsck -fn` clean once the server's buffers were flushed.

## Evidence

- `mke2fs -V`, the pool's zeroing flag, the block group counts from `dumpe2fs`.
- Both write counter readings and their differences.
- The `e2fsck -fn` output and exit code.

## Cleanup

- Delete namespace `lvmo-lazyinit` and check that the volume's LV is gone from the storage server.

## Observations

- The LV's thin pool usage (`lvs -o data_percent`) after step 2 and after step 6.
- Whether `ext4lazyinit` ran at all on the node at step 6.

## Design notes

- The option is used only when the pool reports zeroing on. With `--zero n`, a partially written chunk keeps whatever a deleted volume left there, so unwritten inode tables could hold old data. In that case, or when e2fsprogs is older than 1.47 and rejects the option, the backend formats as before.
- XFS needs nothing: it allocates inodes on demand and has no lazy initialization.
