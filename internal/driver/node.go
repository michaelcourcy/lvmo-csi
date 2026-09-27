package driver

import (
	"context"
	"encoding/json"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"github.com/michaelcourcy/lvmo-csi/internal/backend"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var nodeMu sync.Mutex

// LVMO_ISCSI_HOST_PROC is only needed for nested container test nodes, whose
// network namespace cannot communicate with the kernel iSCSI netlink service.
func nodeCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	if host := os.Getenv("LVMO_ISCSI_HOST_PROC"); name == "iscsiadm" && host != "" {
		args = append([]string{"--mount=" + host + "/1/ns/mnt", "--net=" + host + "/1/ns/net", "--", name}, args...)
		name = "nsenter"
	}
	return exec.CommandContext(ctx, name, args...)
}
func iscsiDevice(portal, iqn string) string {
	prefix := ""
	if host := os.Getenv("LVMO_ISCSI_HOST_PROC"); host != "" {
		prefix = host + "/1/root"
	}
	return prefix + "/dev/disk/by-path/ip-" + portal + "-iscsi-" + iqn + "-lun-0"
}
func run(ctx context.Context, cmd string, args ...string) error {
	out, e := nodeCommand(ctx, cmd, args...).CombinedOutput()
	if e != nil {
		return status.Errorf(codes.Internal, "%s: %v: %s", cmd, e, out)
	}
	return nil
}
func mounted(ctx context.Context, path string) bool {
	return exec.CommandContext(ctx, "mountpoint", "-q", path).Run() == nil
}
func nodeState(path string) string {
	return filepath.Join("/var/lib/lvmo-node", backend.ID("stage-", path)+".json")
}
func (d *Driver) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{NodeId: d.NodeID}, nil
}
func (d *Driver) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	out := &csi.NodeGetCapabilitiesResponse{}
	for _, t := range []csi.NodeServiceCapability_RPC_Type{csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME, csi.NodeServiceCapability_RPC_GET_VOLUME_STATS, csi.NodeServiceCapability_RPC_EXPAND_VOLUME, csi.NodeServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER} {
		out.Capabilities = append(out.Capabilities, &csi.NodeServiceCapability{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{Type: t}}})
	}
	return out, nil
}
func (d *Driver) NodeStageVolume(ctx context.Context, r *csi.NodeStageVolumeRequest) (response *csi.NodeStageVolumeResponse, resultErr error) {
	nodeMu.Lock()
	defer nodeMu.Unlock()
	if r.VolumeId == "" || !filepath.IsAbs(r.StagingTargetPath) || r.VolumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "id, absolute staging path, and capability required")
	}
	v, e := d.API.GetVolume(ctx, &pb.ID{Id: r.VolumeId})
	if e != nil {
		return nil, e
	}
	if e = validate([]*csi.VolumeCapability{r.VolumeCapability}, v.Protocol); e != nil {
		return nil, e
	}
	if saved, err := os.ReadFile(nodeState(r.StagingTargetPath)); err == nil {
		previous := &pb.Volume{}
		if err = json.Unmarshal(saved, previous); err != nil {
			return nil, err
		}
		if previous.Id != r.VolumeId {
			return nil, status.Error(codes.FailedPrecondition, "stage belongs to another volume")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if e = os.MkdirAll(r.StagingTargetPath, 0750); e != nil {
		return nil, e
	}
	if e = os.MkdirAll("/var/lib/lvmo-node", 0700); e != nil {
		return nil, e
	}
	state, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	if e = os.WriteFile(nodeState(r.StagingTargetPath), state, 0600); e != nil {
		return nil, e
	}
	if v.Protocol == "nfs" {
		if !mounted(ctx, r.StagingTargetPath) {
			opts := []string{"vers=4.1"}
			if r.VolumeCapability.AccessMode.Mode == csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY {
				opts = append(opts, "ro")
			}
			if m := r.VolumeCapability.GetMount(); m != nil {
				opts = append(opts, m.MountFlags...)
			}
			if e = run(ctx, "mount", "-t", "nfs", "-o", strings.Join(opts, ","), v.Server+":"+v.Path, r.StagingTargetPath); e != nil {
				return nil, e
			}
		}
		return &csi.NodeStageVolumeResponse{}, nil
	}
	portal := v.Server
	if !strings.Contains(portal, ":") {
		portal += ":3260"
	}
	alreadyStaged := mounted(ctx, r.StagingTargetPath) || mounted(ctx, filepath.Join(r.StagingTargetPath, "block"))
	defer func() {
		if resultErr == nil || alreadyStaged {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		for _, path := range []string{filepath.Join(r.StagingTargetPath, "block"), r.StagingTargetPath} {
			if mounted(cleanup, path) {
				_ = run(cleanup, "umount", path)
			}
		}
		_ = run(cleanup, "iscsiadm", "-m", "node", "-T", v.Iqn, "-p", portal, "--logout")
	}()
	dev := iscsiDevice(portal, v.Iqn)
	if _, e = os.Stat(dev); e != nil {
		if e = run(ctx, "iscsiadm", "-m", "discovery", "-t", "sendtargets", "-p", portal); e != nil {
			return nil, e
		}
		if e = run(ctx, "iscsiadm", "-m", "node", "-T", v.Iqn, "-p", portal, "--login"); e != nil {
			if _, se := os.Stat(dev); se != nil {
				return nil, e
			}
		}
	}
	for i := 0; i < 50; i++ {
		if _, e = os.Stat(dev); e == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if e != nil {
		return nil, status.Error(codes.Unavailable, "iSCSI device did not appear")
	}
	if os.Getenv("LVMO_ISCSI_HOST_PROC") != "" {
		var st unix.Stat_t
		if err := unix.Stat(dev, &st); err != nil {
			return nil, err
		}
		dev = "/dev/lvmo-" + v.Id
		_ = os.Remove(dev)
		if err := unix.Mknod(dev, unix.S_IFBLK|0600, int(st.Rdev)); err != nil {
			return nil, err
		}
	}
	if r.VolumeCapability.GetBlock() != nil {
		target := filepath.Join(r.StagingTargetPath, "block")
		if !mounted(ctx, target) {
			f, e := os.OpenFile(target, os.O_CREATE|os.O_RDWR, 0600)
			if e != nil {
				return nil, e
			}
			f.Close()
			if e = run(ctx, "mount", "--bind", dev, target); e != nil {
				return nil, e
			}
		}
	} else if !mounted(ctx, r.StagingTargetPath) {
		fs := v.Filesystem
		if fs == "" {
			return nil, status.Error(codes.FailedPrecondition, "volume has no filesystem")
		}
		opts := append([]string{}, r.VolumeCapability.GetMount().MountFlags...)
		readonly := r.VolumeCapability.AccessMode.Mode == csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY
		if readonly {
			opts = append(opts, "ro")
		}
		if fs == "xfs" {
			opts = append(opts, "nouuid")
		}
		if len(opts) == 0 {
			opts = []string{"defaults"}
		}
		if e = run(ctx, "mount", "-t", fs, "-o", strings.Join(opts, ","), dev, r.StagingTargetPath); e != nil {
			return nil, e
		}
		// A snapshot restore may request a larger LV than the original filesystem.
		if readonly {
			return &csi.NodeStageVolumeResponse{}, nil
		}
		if fs == "xfs" {
			e = run(ctx, "xfs_growfs", r.StagingTargetPath)
		} else {
			e = run(ctx, "resize2fs", dev)
		}
		if e != nil {
			return nil, e
		}
	}
	return &csi.NodeStageVolumeResponse{}, nil
}
func (d *Driver) NodePublishVolume(ctx context.Context, r *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	nodeMu.Lock()
	defer nodeMu.Unlock()
	if r.VolumeId == "" || !filepath.IsAbs(r.TargetPath) || !filepath.IsAbs(r.StagingTargetPath) || r.VolumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "id, paths, and capability required")
	}
	saved, err := os.ReadFile(nodeState(r.StagingTargetPath))
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "volume not staged")
	}
	staged := &pb.Volume{}
	if err = json.Unmarshal(saved, staged); err != nil {
		return nil, err
	}
	if staged.Id != r.VolumeId {
		return nil, status.Error(codes.FailedPrecondition, "stage belongs to another volume")
	}
	source := r.StagingTargetPath
	var e error
	if r.VolumeCapability.GetBlock() != nil {
		source = filepath.Join(source, "block")
		e = os.MkdirAll(filepath.Dir(r.TargetPath), 0750)
		if e == nil {
			var f *os.File
			f, e = os.OpenFile(r.TargetPath, os.O_CREATE|os.O_RDWR, 0600)
			if e == nil {
				f.Close()
			}
		}
	} else {
		e = os.MkdirAll(r.TargetPath, 0750)
	}
	if e != nil {
		return nil, e
	}
	if mounted(ctx, r.TargetPath) {
		var from, to unix.Stat_t
		if unix.Stat(source, &from) != nil || unix.Stat(r.TargetPath, &to) != nil || from.Dev != to.Dev || from.Ino != to.Ino {
			return nil, status.Error(codes.AlreadyExists, "target belongs to another mount")
		}
		return &csi.NodePublishVolumeResponse{}, nil
	}
	if !mounted(ctx, source) {
		return nil, status.Error(codes.FailedPrecondition, "volume not staged")
	}
	if e = run(ctx, "mount", "--bind", source, r.TargetPath); e != nil {
		return nil, e
	}
	if r.Readonly || r.VolumeCapability.GetAccessMode().GetMode() == csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY || r.VolumeCapability.GetAccessMode().GetMode() == csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY {
		if e = run(ctx, "mount", "-o", "remount,bind,ro", r.TargetPath); e != nil {
			return nil, e
		}
	}
	return &csi.NodePublishVolumeResponse{}, nil
}
func (d *Driver) NodeUnpublishVolume(ctx context.Context, r *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	nodeMu.Lock()
	defer nodeMu.Unlock()
	if r.VolumeId == "" || !filepath.IsAbs(r.TargetPath) {
		return nil, status.Error(codes.InvalidArgument, "id and target required")
	}
	if mounted(ctx, r.TargetPath) {
		if e := run(ctx, "umount", r.TargetPath); e != nil {
			return nil, e
		}
	}
	if e := os.Remove(r.TargetPath); e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}
func (d *Driver) NodeUnstageVolume(ctx context.Context, r *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	nodeMu.Lock()
	defer nodeMu.Unlock()
	if r.VolumeId == "" || !filepath.IsAbs(r.StagingTargetPath) {
		return nil, status.Error(codes.InvalidArgument, "id and staging path required")
	}
	if saved, err := os.ReadFile(nodeState(r.StagingTargetPath)); err == nil {
		previous := &pb.Volume{}
		if err = json.Unmarshal(saved, previous); err != nil {
			return nil, err
		}
		if previous.Id != r.VolumeId {
			return nil, status.Error(codes.FailedPrecondition, "stage belongs to another volume")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	for _, path := range []string{filepath.Join(r.StagingTargetPath, "block"), r.StagingTargetPath} {
		if mounted(ctx, path) {
			if e := run(ctx, "umount", path); e != nil {
				return nil, e
			}
		}
	}
	data, e := os.ReadFile(nodeState(r.StagingTargetPath))
	if e == nil {
		v := &pb.Volume{}
		if e = json.Unmarshal(data, v); e != nil {
			return nil, e
		}
		if v.Id != r.VolumeId {
			return nil, status.Error(codes.FailedPrecondition, "stage belongs to another volume")
		}
		if v.Protocol == "iscsi" {
			portal := v.Server
			if !strings.Contains(portal, ":") {
				portal += ":3260"
			} // Exit 21 means no matching session/node remains.
			for _, op := range [][]string{{"--logout"}, {"-o", "delete"}} {
				args := append([]string{"-m", "node", "-T", v.Iqn, "-p", portal}, op...)
				out, err := nodeCommand(ctx, "iscsiadm", args...).CombinedOutput()
				if err != nil {
					if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 21 {
						return nil, status.Errorf(codes.Internal, "iscsi cleanup: %v: %s", err, out)
					}
				}
			}
		}
		if os.Getenv("LVMO_ISCSI_HOST_PROC") != "" {
			_ = os.Remove("/dev/lvmo-" + v.Id)
		}
		os.Remove(nodeState(r.StagingTargetPath))
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	os.Remove(filepath.Join(r.StagingTargetPath, "block"))
	os.Remove(r.StagingTargetPath)
	return &csi.NodeUnstageVolumeResponse{}, nil
}
func (d *Driver) NodeGetVolumeStats(ctx context.Context, r *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	if r.VolumeId == "" || r.VolumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "id and path required")
	}
	info, e := os.Stat(r.VolumePath)
	if os.IsNotExist(e) {
		return nil, status.Error(codes.NotFound, "volume path not found")
	}
	if e != nil {
		return nil, e
	}
	if info.Mode()&os.ModeDevice != 0 {
		v, e := d.API.GetVolume(ctx, &pb.ID{Id: r.VolumeId})
		if e != nil {
			return nil, e
		}
		return &csi.NodeGetVolumeStatsResponse{Usage: []*csi.VolumeUsage{{Total: v.Bytes, Unit: csi.VolumeUsage_BYTES}}}, nil
	}
	var st unix.Statfs_t
	if e = unix.Statfs(r.VolumePath, &st); e != nil {
		return nil, e
	}
	return &csi.NodeGetVolumeStatsResponse{Usage: []*csi.VolumeUsage{{Total: int64(st.Blocks) * int64(st.Bsize), Available: int64(st.Bavail) * int64(st.Bsize), Used: int64(st.Blocks-st.Bfree) * int64(st.Bsize), Unit: csi.VolumeUsage_BYTES}, {Total: int64(st.Files), Available: int64(st.Ffree), Used: int64(st.Files - st.Ffree), Unit: csi.VolumeUsage_INODES}}}, nil
}
func (d *Driver) NodeExpandVolume(ctx context.Context, r *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	nodeMu.Lock()
	defer nodeMu.Unlock()
	if r.VolumeId == "" || r.VolumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "id, path, capacity required")
	}
	v, e := d.API.GetVolume(ctx, &pb.ID{Id: r.VolumeId})
	if e != nil {
		return nil, e
	}
	if v.Protocol == "iscsi" {
		if e = run(ctx, "iscsiadm", "-m", "session", "-R"); e != nil {
			return nil, e
		}
		if !v.Block {
			portal := v.Server
			if !strings.Contains(portal, ":") {
				portal += ":3260"
			}
			dev := iscsiDevice(portal, v.Iqn)
			if os.Getenv("LVMO_ISCSI_HOST_PROC") != "" {
				dev = "/dev/lvmo-" + v.Id
			}
			if v.Filesystem == "xfs" {
				e = run(ctx, "xfs_growfs", r.VolumePath)
			} else {
				e = run(ctx, "resize2fs", dev)
			}
			if e != nil {
				return nil, e
			}
		}
	}
	return &csi.NodeExpandVolumeResponse{CapacityBytes: v.Bytes}, nil
}
