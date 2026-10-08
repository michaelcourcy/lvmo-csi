package driver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"github.com/michaelcourcy/lvmo-csi/internal/backend"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var nodeMu sync.Mutex

// LVMO_ISCSI_HOST_PROC is only needed for nested container test nodes, whose
// network namespace cannot communicate with the kernel iSCSI netlink service.
func nodeCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	host := os.Getenv("LVMO_ISCSI_HOST_PROC")
	switch {
	case name == "iscsiadm" && host != "":
		args = append([]string{"--mount=" + host + "/1/ns/mnt", "--net=" + host + "/1/ns/net", "--", name}, args...)
		name = "nsenter"
	case name == "modprobe" && host != "":
		args = append([]string{"--mount=" + host + "/1/ns/mnt", "--", name}, args...)
		name = "nsenter"
	case name == "modprobe":
		// The node plugin shares the host's PID namespace: PID 1 is the host's.
		args = append([]string{"--target=1", "--mount", "--", name}, args...)
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

// cmdsMax sizes a session's command pool for a queue depth: Linux keeps 15
// slots for task management and requires a power of two of at least 16.
func cmdsMax(depth int) int {
	n := 16
	for n < depth+15 && n < 2048 {
		n *= 2
	}
	return n
}

// setCommandTimeout sets the SCSI command timeout of the disk behind an
// iSCSI by-path link, so that queued commands are not aborted too early.
func setCommandTimeout(dev string, seconds int) error {
	var path string
	if os.Getenv("LVMO_ISCSI_HOST_PROC") != "" {
		// /proc/<pid>/root is a kernel magic link. EvalSymlinks would turn
		// it into this container's root, losing the host device namespace.
		var st unix.Stat_t
		if err := unix.Stat(dev, &st); err != nil {
			return err
		}
		path = os.Getenv("LVMO_ISCSI_HOST_PROC") + "/1/root" + fmt.Sprintf("/sys/dev/block/%d:%d/device/timeout", unix.Major(uint64(st.Rdev)), unix.Minor(uint64(st.Rdev)))
	} else {
		disk, err := filepath.EvalSymlinks(dev)
		if err != nil {
			return err
		}
		path = "/sys/block/" + filepath.Base(disk) + "/device/timeout"
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(seconds)), 0644); err != nil {
		return status.Errorf(codes.Internal, "set SCSI timeout %s: %v", path, err)
	}
	return nil
}

const nodeDir = "/var/lib/lvmo-node"

// NodeInitiator returns lvmo's own iSCSI initiator name on this node,
// generating it the first time. It is kept on the host, so that it survives
// pod restarts and reboots, and it never depends on the host's
// /etc/iscsi/initiatorname.iscsi, which nodes cloned from one image share.
func NodeInitiator(node string) (string, error) {
	path := filepath.Join(nodeDir, "initiatorname")
	if data, err := os.ReadFile(path); err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			return name, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	name := newInitiator(node, hex.EncodeToString(random))
	if err := os.MkdirAll(nodeDir, 0700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(name+"\n"), 0600); err != nil {
		return "", err
	}
	return name, os.Rename(tmp, path)
}

// newInitiator builds an IQN from the node name, for readability on the
// storage server, and a random suffix, for uniqueness: node names come back
// when nodes are replaced.
func newInitiator(node, random string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(node) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			b.WriteRune(r)
		}
		if b.Len() == 63 {
			break
		}
	}
	return "iqn.2026-09.io.lvmo.node:" + b.String() + ":" + random
}

// ifaceName is the open-iscsi iface record through which lvmo logs in: it
// carries lvmo's initiator name, and leaves the host's default iface alone.
// It is derived from the initiator so that nested test nodes sharing one
// iSCSI database each get their own.
func ifaceName(initiator string) string { return backend.ID("lvmo-", initiator)[:21] }

// ensureIface creates or corrects lvmo's iface record.
func ensureIface(ctx context.Context, iface, initiator string) error {
	out, err := nodeCommand(ctx, "iscsiadm", "-m", "iface", "-I", iface).CombinedOutput()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "iface.initiatorname = "); ok && strings.TrimSpace(value) == initiator {
				return nil
			}
		}
	} else if e := run(ctx, "iscsiadm", "-m", "iface", "-I", iface, "-o", "new"); e != nil {
		return e
	}
	return run(ctx, "iscsiadm", "-m", "iface", "-I", iface, "-o", "update", "-n", "iface.initiatorname", "-v", initiator)
}

// LoadISCSIModule loads iscsi_tcp in the host's mount namespace: iscsiadm in
// this container cannot load host modules, and some distributions ship the
// module without loading it.
func LoadISCSIModule(ctx context.Context) error {
	if _, err := os.Stat("/sys/module/iscsi_tcp"); err == nil {
		return nil
	}
	return run(ctx, "modprobe", "iscsi_tcp")
}
func nodeState(path string) string {
	return filepath.Join(nodeDir, backend.ID("stage-", path)+".json")
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
	_, initiator, _ := parseNodeID(d.NodeID)
	if initiator == "" {
		return nil, status.Error(codes.FailedPrecondition, "this node has no iSCSI initiator name")
	}
	iface := ifaceName(initiator)
	if _, err := d.API.AcquireVolume(ctx, &pb.VolumeLease{VolumeId: v.Id, NodeId: d.NodeID}); err != nil {
		return nil, err
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
		if mounted(cleanup, r.StagingTargetPath) || mounted(cleanup, filepath.Join(r.StagingTargetPath, "block")) {
			return
		}
		if err := nodeCommand(cleanup, "iscsiadm", "-m", "node", "-T", v.Iqn, "-p", portal, "-I", iface, "--logout").Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 21 {
				return
			}
		}
		_, _ = d.API.ReleaseVolume(cleanup, &pb.VolumeLease{VolumeId: v.Id, NodeId: d.NodeID})
	}()
	dev := iscsiDevice(portal, v.Iqn)
	if _, e = os.Stat(dev); e != nil {
		if e = ensureIface(ctx, iface, initiator); e != nil {
			return nil, e
		}
		// Discovery through lvmo's iface binds the node records to it, and
		// presents lvmo's initiator, which the target's access list admits.
		if e = run(ctx, "iscsiadm", "-m", "discovery", "-t", "sendtargets", "-p", portal, "-I", iface); e != nil {
			return nil, e
		}
		// Match the target's session window, so that an overloaded storage
		// server queues commands instead of letting them time out.
		if depth := int(v.IscsiQueueDepth); depth > 0 {
			for name, value := range map[string]int{"node.session.queue_depth": depth, "node.session.cmds_max": cmdsMax(depth)} {
				if e = run(ctx, "iscsiadm", "-m", "node", "-T", v.Iqn, "-p", portal, "-I", iface, "-o", "update", "-n", name, "-v", strconv.Itoa(value)); e != nil {
					return nil, e
				}
			}
		}
		if e = run(ctx, "iscsiadm", "-m", "node", "-T", v.Iqn, "-p", portal, "-I", iface, "--login"); e != nil {
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
	if v.IscsiCommandTimeoutSeconds > 0 {
		if e = setCommandTimeout(dev, int(v.IscsiCommandTimeoutSeconds)); e != nil {
			return nil, e
		}
	}
	if os.Getenv("LVMO_ISCSI_HOST_PROC") != "" {
		var st unix.Stat_t
		if err := unix.Stat(dev, &st); err != nil {
			return nil, err
		}
		dev = "/dev/" + backend.ID("lvmo-", v.Id)
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
				if _, initiator, _ := parseNodeID(d.NodeID); initiator != "" {
					args = append(args, "-I", ifaceName(initiator))
				}
				out, err := nodeCommand(ctx, "iscsiadm", args...).CombinedOutput()
				if err != nil {
					if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 21 {
						return nil, status.Errorf(codes.Internal, "iscsi cleanup: %v: %s", err, out)
					}
				}
			}
		}
		if v.Protocol == "iscsi" {
			if _, err := d.API.ReleaseVolume(ctx, &pb.VolumeLease{VolumeId: v.Id, NodeId: d.NodeID}); err != nil {
				return nil, err
			}
		}
		if os.Getenv("LVMO_ISCSI_HOST_PROC") != "" {
			_ = os.Remove("/dev/" + backend.ID("lvmo-", v.Id))
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
				dev = "/dev/" + backend.ID("lvmo-", v.Id)
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

// SendHeartbeats tells every storage server that this node is alive, so that
// failover never fences a node that can still reach its storage.
func (d *Driver) SendHeartbeats(ctx context.Context, interval time.Duration) {
	failing := false
	for {
		call, cancel := context.WithTimeout(ctx, interval)
		_, err := d.API.Heartbeat(call, &pb.NodeHeartbeat{NodeId: d.NodeID})
		cancel()
		if (err != nil) != failing {
			failing = err != nil
			if failing {
				log.Printf("heartbeat failing: %v", err)
			} else {
				log.Printf("heartbeat restored")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
