// Package backend manages exclusively lvmo-owned thin LVs and their exports.
package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type Exec struct{}

func (Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type Config struct {
	Root, Server, Pool, Clients string
	NFSInsecure                 bool
	VGs                         []string
	// ISCSIQueueDepth bounds the commands in flight per iSCSI session, on the
	// target and (through GetVolume) on the node, so that an overloaded disk
	// queues work instead of timing commands out. 0 keeps the defaults.
	ISCSIQueueDepth int
	// ISCSICommandTimeout is the SCSI command timeout nodes set on lvmo disks.
	ISCSICommandTimeout time.Duration
}
type state struct {
	SnapshotDeleting  map[string]bool         `json:"snapshot_deleting,omitempty"`
	Owners            map[string]string       `json:"owners,omitempty"`
	SnapshotOrder     map[string]uint64       `json:"snapshot_order,omitempty"`
	NextSnapshotOrder uint64                  `json:"next_snapshot_order,omitempty"`
	Deleting          map[string]bool         `json:"deleting,omitempty"`
	Volumes           map[string]*pb.Volume   `json:"volumes"`
	Snapshots         map[string]*pb.Snapshot `json:"snapshots"`
	// Attachments maps an iSCSI volume to the nodes allowed to reach it (node ID
	// to initiator). A present entry, even empty, keeps the target in ACL mode.
	Attachments map[string]map[string]string `json:"attachments,omitempty"`
}
type Backend struct {
	pb.UnimplementedStorageServer
	mu         sync.Mutex
	cfg        Config
	run        Runner
	state      state
	lastExport int64
	// Heartbeats are kept in memory: after a restart every node gets a full
	// timeout from the start time before it can be fenced.
	heartbeats map[string]time.Time
	started    time.Time
	now        func() time.Time
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,100}$`)
var validInitiator = regexp.MustCompile(`^(iqn\.[0-9]{4}-[0-9]{2}\.[A-Za-z0-9.-]+(:[A-Za-z0-9.:_-]+)?|eui\.[0-9A-Fa-f]{16}|naa\.[0-9A-Fa-f]{16,32})$`)

func New(cfg Config, run Runner) (*Backend, error) {
	if cfg.Root == "" || !filepath.IsAbs(cfg.Root) || strings.ContainsAny(cfg.Root, " \n\t") {
		return nil, fmt.Errorf("root must be an absolute path without whitespace")
	}
	if cfg.Server == "" || strings.ContainsAny(cfg.Server, "/ \n\t") {
		return nil, fmt.Errorf("server address required")
	}
	if cfg.Pool == "" {
		cfg.Pool = "lvmo-pool"
	}
	if !validName.MatchString(cfg.Pool) {
		return nil, fmt.Errorf("invalid pool name")
	}
	if len(cfg.VGs) == 0 {
		return nil, fmt.Errorf("at least one VG required")
	}
	for _, vg := range cfg.VGs {
		if !validName.MatchString(vg) {
			return nil, fmt.Errorf("invalid VG")
		}
	}
	if cfg.Clients == "" {
		cfg.Clients = "*"
	}
	if cfg.ISCSIQueueDepth < 0 || cfg.ISCSIQueueDepth > 512 {
		return nil, fmt.Errorf("iSCSI queue depth must be between 0 and 512")
	}
	if cfg.ISCSICommandTimeout < 0 {
		return nil, fmt.Errorf("iSCSI command timeout must not be negative")
	}
	if strings.ContainsAny(cfg.Clients, " \n\t()") {
		return nil, fmt.Errorf("invalid NFS client selector")
	}
	b := &Backend{cfg: cfg, run: run, state: state{Volumes: map[string]*pb.Volume{}, Snapshots: map[string]*pb.Snapshot{}}, heartbeats: map[string]time.Time{}, now: time.Now}
	b.started = b.now()
	if err := os.MkdirAll(cfg.Root, 0700); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(cfg.Root, "state.json"))
	if err == nil {
		err = json.Unmarshal(data, &b.state)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if b.state.SnapshotDeleting == nil {
		b.state.SnapshotDeleting = map[string]bool{}
	}
	if b.state.Owners == nil {
		b.state.Owners = map[string]string{}
	}
	if b.state.Attachments == nil {
		b.state.Attachments = map[string]map[string]string{}
	}
	if b.state.SnapshotOrder == nil {
		b.state.SnapshotOrder = map[string]uint64{}
	}
	if b.state.Deleting == nil {
		b.state.Deleting = map[string]bool{}
	}
	return b, nil
}
func (b *Backend) save() error {
	data, err := json.MarshalIndent(b.state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(b.cfg.Root, ".state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(b.cfg.Root, "state.json")); err != nil {
		return err
	}
	d, err := os.Open(b.cfg.Root)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func ID(prefix, name string) string {
	sum := sha256.Sum256([]byte(name))
	return prefix + hex.EncodeToString(sum[:16])
}
func (b *Backend) vg(v string) (string, error) {
	if v == "" && len(b.cfg.VGs) == 1 {
		v = b.cfg.VGs[0]
	}
	for _, x := range b.cfg.VGs {
		if x == v {
			return v, nil
		}
	}
	return "", status.Error(codes.InvalidArgument, "select a configured VG")
}
func device(vg, id string) string { return "/dev/" + vg + "/" + id }
func internal(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	if status.Code(err) != codes.Unknown {
		return err
	}
	return status.Error(codes.Internal, err.Error())
}
func (b *Backend) cmd(ctx context.Context, name string, args ...string) error {
	_, err := b.run.Run(ctx, name, args...)
	return err
}
func round(n int64) int64 {
	const extent = 4 * 1024 * 1024
	if n < extent {
		return extent
	}
	return (n + extent - 1) / extent * extent
}
func (b *Backend) CreateVolume(ctx context.Context, r *pb.CreateVolumeRequest) (*pb.Volume, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.Name == "" || r.Bytes < 0 || r.Bytes > 1<<60 {
		return nil, status.Error(codes.InvalidArgument, "name and valid capacity required")
	}
	vg, err := b.vg(r.Vg)
	if err != nil {
		return nil, err
	}
	if r.Protocol != "nfs" && r.Protocol != "iscsi" {
		return nil, status.Error(codes.InvalidArgument, "protocol must be nfs or iscsi")
	}
	if r.Block && r.Protocol == "nfs" {
		return nil, status.Error(codes.InvalidArgument, "NFS does not support block access")
	}
	fs := r.Filesystem
	if r.Block {
		// A StorageClass filesystem preference must never format a new raw device.
		// Snapshot/volume clones recover their source filesystem metadata below.
		fs = ""
	}
	if fs == "" && !r.Block {
		fs = "ext4"
	}
	if fs != "" && fs != "ext4" && fs != "xfs" {
		return nil, status.Error(codes.InvalidArgument, "filesystem must be ext4 or xfs")
	}
	if r.SourceSnapshot != "" && r.SourceVolume != "" {
		return nil, status.Error(codes.InvalidArgument, "only one source allowed")
	}
	var src, lineage string
	size := round(r.Bytes)
	if r.SourceSnapshot != "" {
		s := b.state.Snapshots[r.SourceSnapshot]
		if s == nil || !s.Ready || b.state.SnapshotDeleting[s.Id] {
			return nil, status.Error(codes.NotFound, "snapshot not found")
		}
		if s.Vg != vg {
			return nil, status.Error(codes.InvalidArgument, "clone must use source VG")
		}
		src = s.Id
		lineage = s.Lineage
		fs = s.Filesystem
		if size < s.Bytes {
			return nil, status.Error(codes.OutOfRange, "clone smaller than source")
		}
	}
	if r.SourceVolume != "" {
		v := b.state.Volumes[r.SourceVolume]
		if v == nil || !v.Ready || b.state.Deleting[v.Id] {
			return nil, status.Error(codes.NotFound, "source not found")
		}
		if v.Vg != vg {
			return nil, status.Error(codes.InvalidArgument, "clone must use source VG")
		}
		src = v.Id
		fs = v.Filesystem
		if size < v.Bytes {
			return nil, status.Error(codes.OutOfRange, "clone smaller than source")
		}
	}
	if !r.Block && fs == "" {
		return nil, status.Error(codes.InvalidArgument, "cannot mount unformatted source")
	}
	id := ID("v-", r.Name)
	if b.state.Deleting[id] {
		return nil, status.Error(codes.Aborted, "previous volume deletion is reclaiming storage; retry")
	}
	if v := b.state.Volumes[id]; v != nil {
		if v.Bytes < size || v.Protocol != r.Protocol || v.Vg != vg || v.Block != r.Block || v.Filesystem != fs || v.SourceSnapshot != r.SourceSnapshot || v.SourceVolume != r.SourceVolume {
			return nil, status.Error(codes.AlreadyExists, "incompatible existing volume")
		}
		if !v.Ready {
			return nil, status.Error(codes.Aborted, "incomplete volume requires reconciliation")
		}
		return proto.Clone(v).(*pb.Volume), nil
	}
	if lineage == "" {
		lineage = id
	}
	v := &pb.Volume{Id: id, Name: r.Name, Bytes: size, Vg: vg, Protocol: r.Protocol, Filesystem: fs, Server: b.cfg.Server, Path: filepath.Join(b.cfg.Root, "volumes", id), Iqn: "iqn.2026-09.io.lvmo:" + id, Lineage: lineage, Block: r.Block, SourceSnapshot: r.SourceSnapshot, SourceVolume: r.SourceVolume}
	b.state.Volumes[id] = v
	if err = b.save(); err != nil {
		delete(b.state.Volumes, id)
		return nil, internal(err)
	}
	if src == "" {
		err = b.cmd(ctx, "lvcreate", "--yes", "--type", "thin", "--virtualsize", fmt.Sprintf("%dB", size), "--thinpool", vg+"/"+b.cfg.Pool, "--name", id, "--addtag", "lvmo")
	} else {
		err = b.cloneLV(ctx, vg, src, id)
	}
	if err != nil {
		return nil, internal(err)
	}
	if src != "" {
		if err = b.cmd(ctx, "lvextend", "--size", fmt.Sprintf("%dB", size), device(vg, id)); err != nil { // LVM returns failure for unchanged size; verify below.
			actual, e := b.lvSize(ctx, vg, id)
			if e != nil || actual != size {
				return nil, internal(err)
			}
		}
	}
	if src == "" && fs != "" {
		if fs == "ext4" {
			err = b.cmd(ctx, "mkfs.ext4", "-F", device(vg, id))
		} else {
			err = b.cmd(ctx, "mkfs.xfs", "-f", device(vg, id))
		}
		if err != nil {
			return nil, internal(err)
		}
	}
	if err = b.publish(ctx, v); err != nil {
		return nil, internal(err)
	}
	if src == "" && v.Protocol == "nfs" {
		if err = os.Chmod(v.Path, 0777); err != nil {
			return nil, internal(err)
		}
	}
	if src != "" && v.Protocol == "nfs" {
		if fs == "xfs" {
			err = b.cmd(ctx, "xfs_growfs", v.Path)
		} else {
			err = b.cmd(ctx, "resize2fs", device(vg, id))
		}
		if err != nil {
			return nil, internal(err)
		}
	}
	v.Ready = true
	if err := b.save(); err != nil {
		v.Ready = false
		return nil, internal(err)
	}
	return proto.Clone(v).(*pb.Volume), nil
}
func (b *Backend) lvSize(ctx context.Context, vg, id string) (int64, error) {
	out, e := b.run.Run(ctx, "lvs", "--noheadings", "--units", "b", "--nosuffix", "-o", "lv_size", device(vg, id))
	if e != nil {
		return 0, e
	}
	f, e := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return int64(f), e
}
func (b *Backend) cloneLV(ctx context.Context, vg, src, id string) error {
	var path string
	if v := b.state.Volumes[src]; v != nil && v.Protocol == "nfs" {
		path = v.Path
	}
	if path != "" {
		if err := b.cmd(ctx, "sync", "-f", path); err != nil {
			return err
		}
	}
	if err := b.cmd(ctx, "lvcreate", "--yes", "--snapshot", "--name", id, "--addtag", "lvmo", device(vg, src)); err != nil {
		return err
	}
	if err := b.cmd(ctx, "lvchange", "--activate", "y", "--ignoreactivationskip", device(vg, id)); err != nil {
		return err
	}
	attr, err := b.run.Run(ctx, "lvs", "--noheadings", "-o", "lv_attr", device(vg, id))
	if err != nil {
		return err
	}
	if strings.HasPrefix(strings.TrimSpace(string(attr)), "Vr") {
		return b.cmd(ctx, "lvchange", "--permission", "rw", device(vg, id))
	}
	return nil
}
func (b *Backend) publish(ctx context.Context, v *pb.Volume) error {
	if v.Protocol == "nfs" {
		if err := os.MkdirAll(v.Path, 0755); err != nil {
			return err
		}
		if err := b.cmd(ctx, "mountpoint", "-q", v.Path); err != nil {
			opts := "defaults"
			if v.Filesystem == "xfs" {
				opts = "nouuid"
			}
			if err = b.cmd(ctx, "mount", "-t", v.Filesystem, "-o", opts, device(v.Vg, v.Id), v.Path); err != nil {
				return err
			}
		}
		if err := os.MkdirAll("/etc/exports.d", 0755); err != nil {
			return err
		}
		line := b.nfsExportLine(v)
		if err := os.WriteFile("/etc/exports.d/lvmo-"+v.Id+".exports", []byte(line), 0644); err != nil {
			return err
		}
		return b.refreshExports(ctx)
	}
	// targetcli operations are idempotent after checking the JSON configuration.
	if err := b.cmd(ctx, "targetcli", "/backstores/block/"+v.Id, "ls"); err != nil {
		if err = b.cmd(ctx, "targetcli", "/backstores/block", "create", v.Id, device(v.Vg, v.Id)); err != nil {
			return err
		}
	}
	if err := b.cmd(ctx, "targetcli", "/backstores/block/"+v.Id, "set", "attribute", "emulate_tpu=1", "emulate_tpws=1"); err != nil {
		return err
	}
	target := "/iscsi/" + v.Iqn
	if err := b.cmd(ctx, "targetcli", target, "ls"); err != nil {
		if err = b.cmd(ctx, "targetcli", "/iscsi", "create", v.Iqn); err != nil {
			return err
		}
	}
	if b.cmd(ctx, "targetcli", target+"/tpg1/luns/lun0", "ls") != nil {
		if err := b.cmd(ctx, "targetcli", target+"/tpg1/luns", "create", "/backstores/block/"+v.Id); err != nil {
			return err
		}
	}
	if err := b.applyACL(ctx, v); err != nil {
		return err
	}
	return b.cmd(ctx, "targetcli", "saveconfig")
}

// applyACL leaves a target open to any initiator until the volume is first
// published to a node. From then on only published initiators may log in, and
// deleting an initiator's ACL closes its sessions: this is how a node is fenced.
func (b *Backend) applyACL(ctx context.Context, v *pb.Volume) error {
	tpg := "/iscsi/" + v.Iqn + "/tpg1"
	allowed, fenced := b.state.Attachments[v.Id]
	// The session window is set before any ACL exists: ACLs, dynamic or
	// explicit, take the portal group's default when they are created.
	depth := []string{}
	if b.cfg.ISCSIQueueDepth > 0 {
		depth = append(depth, "default_cmdsn_depth="+strconv.Itoa(b.cfg.ISCSIQueueDepth))
	}
	if !fenced {
		return b.cmd(ctx, "targetcli", append([]string{tpg, "set", "attribute", "authentication=0", "generate_node_acls=1", "demo_mode_write_protect=0", "cache_dynamic_acls=1"}, depth...)...)
	}
	if err := b.cmd(ctx, "targetcli", append([]string{tpg, "set", "attribute", "authentication=0", "generate_node_acls=0", "cache_dynamic_acls=0"}, depth...)...); err != nil {
		return err
	}
	want := map[string]bool{}
	for _, initiator := range allowed {
		want[initiator] = true
	}
	out, err := b.run.Run(ctx, "targetcli", tpg+"/acls", "ls")
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "o- ")); len(f) > 0 && validInitiator.MatchString(f[0]) {
			present[f[0]] = true
		}
	}
	for initiator := range present {
		if !want[initiator] {
			if err := b.cmd(ctx, "targetcli", tpg+"/acls", "delete", initiator); err != nil {
				return err
			}
		}
	}
	for initiator := range want {
		if !present[initiator] {
			if err := b.cmd(ctx, "targetcli", tpg+"/acls", "create", initiator); err != nil {
				return err
			}
		}
	}
	return nil
}

// PublishVolume grants one node's initiator access to an iSCSI volume. Volumes
// are single-node, so a second node is refused until the first is unpublished.
func (b *Backend) PublishVolume(ctx context.Context, r *pb.VolumePublish) (*pb.Empty, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.VolumeId == "" || r.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume and node required")
	}
	v := b.state.Volumes[r.VolumeId]
	if v == nil || !v.Ready || b.state.Deleting[r.VolumeId] {
		return nil, status.Error(codes.NotFound, "volume not found")
	}
	if v.Protocol != "iscsi" {
		return &pb.Empty{}, nil
	}
	if !validInitiator.MatchString(r.Initiator) {
		return nil, status.Error(codes.FailedPrecondition, "node has no usable iSCSI initiator name")
	}
	allowed, fenced := b.state.Attachments[v.Id]
	for node := range allowed {
		if node != r.NodeId {
			return nil, status.Error(codes.FailedPrecondition, "iSCSI volume is published to another node")
		}
	}
	previous, had := allowed[r.NodeId]
	if !fenced {
		allowed = map[string]string{}
		b.state.Attachments[v.Id] = allowed
	}
	allowed[r.NodeId] = r.Initiator
	if err := b.save(); err != nil {
		if had {
			allowed[r.NodeId] = previous
		} else {
			delete(allowed, r.NodeId)
		}
		if !fenced {
			delete(b.state.Attachments, v.Id)
		}
		return nil, internal(err)
	}
	if err := b.applyACL(ctx, v); err != nil {
		return nil, internal(err)
	}
	return &pb.Empty{}, internal(b.cmd(ctx, "targetcli", "saveconfig"))
}

func (b *Backend) Heartbeat(ctx context.Context, r *pb.NodeHeartbeat) (*pb.Empty, error) {
	if r.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "node required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.heartbeats[r.NodeId] = b.now()
	return &pb.Empty{}, nil
}

// FenceNode revokes a node from every iSCSI target it is published to and
// releases its ownership. The caller decides the node is dead; this server only
// agrees if the node has not sent a heartbeat for the given silence.
func (b *Backend) FenceNode(ctx context.Context, r *pb.NodeFence) (*pb.FencedVolumes, error) {
	if (r.NodeId == "") == (r.NodeName == "") || r.SilenceSeconds <= 0 {
		return nil, status.Error(codes.InvalidArgument, "one of node ID and node name, and silence required")
	}
	matches := func(id string) bool {
		if r.NodeId != "" {
			return id == r.NodeId
		}
		rest, ok := strings.CutPrefix(id, "lvmo:")
		name, _, _ := strings.Cut(rest, ":")
		return ok && name == r.NodeName
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	last := b.started
	for id, seen := range b.heartbeats {
		if matches(id) && seen.After(last) {
			last = seen
		}
	}
	if silent := b.now().Sub(last); silent < time.Duration(r.SilenceSeconds)*time.Second {
		return nil, status.Errorf(codes.FailedPrecondition, "node was heard %s ago", silent.Round(time.Second))
	}
	out := &pb.FencedVolumes{}
	for id, allowed := range b.state.Attachments {
		for node := range allowed {
			if matches(node) {
				delete(allowed, node)
				out.VolumeIds = append(out.VolumeIds, id)
			}
		}
	}
	for id, owner := range b.state.Owners {
		if matches(owner) {
			delete(b.state.Owners, id)
			if _, listed := b.state.Attachments[id]; !listed {
				out.VolumeIds = append(out.VolumeIds, id)
			}
		}
	}
	if len(out.VolumeIds) == 0 {
		return out, nil
	}
	sort.Strings(out.VolumeIds)
	if err := b.save(); err != nil {
		return nil, internal(err)
	}
	for _, id := range out.VolumeIds {
		if v := b.state.Volumes[id]; v != nil && v.Protocol == "iscsi" {
			if err := b.applyACL(ctx, v); err != nil {
				return nil, internal(err)
			}
		}
	}
	return out, internal(b.cmd(ctx, "targetcli", "saveconfig"))
}

// UnpublishVolume revokes a node, or every node when none is named. The
// revoked node can no longer reach the target, so its staging ownership is
// released too: this lets another node take over after a confirmed failure.
func (b *Backend) UnpublishVolume(ctx context.Context, r *pb.VolumePublish) (*pb.Empty, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume required")
	}
	v := b.state.Volumes[r.VolumeId]
	allowed, fenced := b.state.Attachments[r.VolumeId]
	if v == nil || v.Protocol != "iscsi" || !fenced {
		return &pb.Empty{}, nil
	}
	for node := range allowed {
		if r.NodeId == "" || node == r.NodeId {
			delete(allowed, node)
		}
	}
	if owner := b.state.Owners[v.Id]; owner != "" && (r.NodeId == "" || owner == r.NodeId) {
		delete(b.state.Owners, v.Id)
	}
	// Persist first: a restart then reapplies the reduced ACL instead of the old one.
	if err := b.save(); err != nil {
		return nil, internal(err)
	}
	if err := b.applyACL(ctx, v); err != nil {
		return nil, internal(err)
	}
	return &pb.Empty{}, internal(b.cmd(ctx, "targetcli", "saveconfig"))
}
func (b *Backend) GetVolume(ctx context.Context, r *pb.ID) (*pb.Volume, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := b.state.Volumes[r.Id]
	if v == nil || !v.Ready || b.state.Deleting[r.Id] {
		return nil, status.Error(codes.NotFound, "volume not found")
	}
	out := proto.Clone(v).(*pb.Volume)
	// Nodes apply the same queue bound and timeout when they log in.
	if out.Protocol == "iscsi" {
		out.IscsiQueueDepth = int32(b.cfg.ISCSIQueueDepth)
		out.IscsiCommandTimeoutSeconds = int32(b.cfg.ISCSICommandTimeout / time.Second)
	}
	return out, nil
}
func (b *Backend) ListVolumes(context.Context, *pb.Empty) (*pb.Volumes, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := &pb.Volumes{}
	for _, v := range b.state.Volumes {
		if v.Ready && !b.state.Deleting[v.Id] {
			out.Volumes = append(out.Volumes, proto.Clone(v).(*pb.Volume))
		}
	}
	sort.Slice(out.Volumes, func(i, j int) bool { return out.Volumes[i].Id < out.Volumes[j].Id })
	return out, nil
}
func (b *Backend) DeleteVolume(ctx context.Context, r *pb.ID) (*pb.Empty, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	v := b.state.Volumes[r.Id]
	if v == nil {
		return &pb.Empty{}, nil
	}
	if b.state.Owners[r.Id] != "" {
		return nil, status.Error(codes.FailedPrecondition, "volume is staged on a node")
	}
	if len(b.state.Attachments[r.Id]) > 0 {
		return nil, status.Error(codes.FailedPrecondition, "volume is published to a node")
	}
	b.state.Deleting[r.Id] = true
	if err := b.save(); err != nil {
		return nil, internal(err)
	}
	if v.Protocol == "nfs" {
		// Removing the exports file plus exportfs -ra is idempotent.

		removed := false
		if err := os.Remove("/etc/exports.d/lvmo-" + v.Id + ".exports"); err == nil {
			removed = true
		} else if !os.IsNotExist(err) {
			return nil, internal(err)
		}
		// Retry a failed etab update, but never flush repeatedly while waiting for
		// the kernel to release an already-unexported filesystem.
		etab, err := os.ReadFile("/var/lib/nfs/etab")
		if err != nil && !os.IsNotExist(err) {
			return nil, internal(err)
		}
		if removed || strings.Contains(string(etab), v.Path+" ") || strings.Contains(string(etab), v.Path+"\t") {
			if err = b.refreshExports(ctx); err != nil {
				return nil, internal(err)
			}
		}
		if b.cmd(ctx, "mountpoint", "-q", v.Path) == nil {
			err := b.cmd(ctx, "umount", v.Path)
			if err != nil {
				if strings.Contains(err.Error(), "busy") {
					return &pb.Empty{}, nil
				}
				return nil, internal(err)
			}
		}
		os.Remove(v.Path)
	} else {
		if b.cmd(ctx, "targetcli", "/iscsi/"+v.Iqn, "ls") == nil {
			if err := b.cmd(ctx, "targetcli", "/iscsi", "delete", v.Iqn); err != nil {
				return nil, internal(err)
			}
		}
		if b.cmd(ctx, "targetcli", "/backstores/block/"+v.Id, "ls") == nil {
			if err := b.cmd(ctx, "targetcli", "/backstores/block", "delete", v.Id); err != nil {
				return nil, internal(err)
			}
		}
		if err := b.cmd(ctx, "targetcli", "saveconfig"); err != nil {
			return nil, internal(err)
		}
	}
	exists, err := b.lvExists(ctx, v.Vg, v.Id)
	if err != nil {
		return nil, internal(err)
	}
	if exists {
		if err := b.cmd(ctx, "lvremove", "--yes", device(v.Vg, v.Id)); err != nil {
			return nil, internal(err)
		}
	}
	delete(b.state.Volumes, r.Id)
	delete(b.state.Deleting, r.Id)
	delete(b.state.Attachments, r.Id)
	return &pb.Empty{}, internal(b.save())
}
func (b *Backend) ExpandVolume(ctx context.Context, r *pb.ExpandRequest) (*pb.Volume, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := b.state.Volumes[r.Id]
	if v == nil || !v.Ready || b.state.Deleting[r.Id] {
		return nil, status.Error(codes.NotFound, "volume not found")
	}
	if r.Bytes <= 0 || r.Bytes > 1<<60 {
		return nil, status.Error(codes.InvalidArgument, "invalid capacity")
	}
	size := round(r.Bytes)
	if size <= v.Bytes {
		return proto.Clone(v).(*pb.Volume), nil
	}
	if err := b.cmd(ctx, "lvextend", "--size", fmt.Sprintf("%dB", size), device(v.Vg, v.Id)); err != nil {
		actual, e := b.lvSize(ctx, v.Vg, v.Id)
		if e != nil || actual != size {
			return nil, internal(err)
		}
	}
	if v.Protocol == "nfs" {
		var err error
		if v.Filesystem == "xfs" {
			err = b.cmd(ctx, "xfs_growfs", v.Path)
		} else {
			err = b.cmd(ctx, "resize2fs", device(v.Vg, v.Id))
		}
		if err != nil {
			return nil, internal(err)
		}
	}
	previous := v.Bytes
	v.Bytes = size
	if err := b.save(); err != nil {
		v.Bytes = previous
		return nil, internal(err)
	}
	return proto.Clone(v).(*pb.Volume), nil
}
func (b *Backend) CreateSnapshot(ctx context.Context, r *pb.SnapshotRequest) (*pb.Snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.Name == "" || r.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "name and source required")
	}
	id := ID("s-", r.Name)
	if b.state.SnapshotDeleting[id] {
		return nil, status.Error(codes.Aborted, "snapshot deletion is in progress")
	}
	if s := b.state.Snapshots[id]; s != nil {
		if s.VolumeId != r.VolumeId {
			return nil, status.Error(codes.AlreadyExists, "different source")
		}
		if !s.Ready {
			return nil, status.Error(codes.Aborted, "incomplete snapshot")
		}
		return proto.Clone(s).(*pb.Snapshot), nil
	}
	v := b.state.Volumes[r.VolumeId]
	if v == nil || !v.Ready || b.state.Deleting[v.Id] {
		return nil, status.Error(codes.NotFound, "source not found")
	}
	s := &pb.Snapshot{Id: id, Name: r.Name, VolumeId: v.Id, Vg: v.Vg, Bytes: v.Bytes, CreatedUnix: time.Now().Unix(), Filesystem: v.Filesystem, Lineage: v.Lineage}
	b.state.NextSnapshotOrder++
	b.state.SnapshotOrder[id] = b.state.NextSnapshotOrder
	b.state.Snapshots[id] = s
	if err := b.save(); err != nil {
		return nil, internal(err)
	}
	if err := b.cloneLV(ctx, v.Vg, v.Id, id); err != nil {
		return nil, internal(err)
	}
	if err := b.cmd(ctx, "lvchange", "--permission", "r", device(s.Vg, s.Id)); err != nil {
		return nil, internal(err)
	}
	s.Ready = true
	if err := b.save(); err != nil {
		s.Ready = false
		return nil, internal(err)
	}
	return proto.Clone(s).(*pb.Snapshot), nil
}
func (b *Backend) DeleteSnapshot(ctx context.Context, r *pb.ID) (*pb.Empty, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	s := b.state.Snapshots[r.Id]
	if s == nil {
		return &pb.Empty{}, nil
	}
	b.state.SnapshotDeleting[s.Id] = true
	if err := b.save(); err != nil {
		return nil, internal(err)
	}
	exists, err := b.lvExists(ctx, s.Vg, s.Id)
	if err != nil {
		return nil, internal(err)
	}
	if exists {
		if err := b.cmd(ctx, "lvremove", "--yes", device(s.Vg, s.Id)); err != nil {
			return nil, internal(err)
		}
	}
	delete(b.state.Snapshots, r.Id)
	delete(b.state.SnapshotOrder, r.Id)
	delete(b.state.SnapshotDeleting, r.Id)
	return &pb.Empty{}, internal(b.save())
}
func (b *Backend) ListSnapshots(context.Context, *pb.Empty) (*pb.Snapshots, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := &pb.Snapshots{}
	for _, s := range b.state.Snapshots {
		if s.Ready && !b.state.SnapshotDeleting[s.Id] {
			out.Snapshots = append(out.Snapshots, proto.Clone(s).(*pb.Snapshot))
		}
	}
	sort.Slice(out.Snapshots, func(i, j int) bool { return out.Snapshots[i].Id < out.Snapshots[j].Id })
	return out, nil
}
func (b *Backend) GetCapacity(ctx context.Context, r *pb.CapacityRequest) (*pb.Capacity, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.Vg == "" {
		var total int64
		for _, vg := range b.cfg.VGs {
			c, e := b.poolCapacity(ctx, vg)
			if e != nil {
				return nil, e
			}
			total += c.AvailableBytes
		}
		return &pb.Capacity{AvailableBytes: total}, nil
	}
	vg, e := b.vg(r.Vg)
	if e != nil {
		return nil, e
	}
	return b.poolCapacity(ctx, vg)
}
func (b *Backend) poolCapacity(ctx context.Context, vg string) (*pb.Capacity, error) {
	out, e := b.run.Run(ctx, "lvs", "--noheadings", "--units", "b", "--nosuffix", "--separator", ",", "-o", "lv_size,data_percent", vg+"/"+b.cfg.Pool)
	if e != nil {
		return nil, internal(e)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(fields) != 2 {
		return nil, status.Error(codes.Internal, "invalid capacity report")
	}
	size, e := strconv.ParseFloat(strings.TrimSpace(fields[0]), 64)
	if e != nil {
		return nil, internal(e)
	}
	used, e := strconv.ParseFloat(strings.TrimSpace(fields[1]), 64)
	if e != nil {
		return nil, internal(e)
	}
	return &pb.Capacity{AvailableBytes: int64(size * (100 - used) / 100)}, nil
}

// Reconcile restores exports after restart. Incomplete objects remain deletable and
// are never silently formatted or advertised as ready.
func (b *Backend) Reconcile(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, vg := range b.cfg.VGs {
		out, e := b.run.Run(ctx, "lvs", "--noheadings", "-o", "segtype", vg+"/"+b.cfg.Pool)
		if e != nil {
			return e
		}
		if strings.TrimSpace(string(out)) != "thin-pool" {
			return fmt.Errorf("%s/%s must be a pre-created thin pool", vg, b.cfg.Pool)
		}
	}
	for _, v := range b.state.Volumes {
		if !v.Ready {
			b.state.Deleting[v.Id] = true
		}
		if v.Ready && !b.state.Deleting[v.Id] {
			if e := b.cmd(ctx, "lvchange", "--activate", "y", "--ignoreactivationskip", device(v.Vg, v.Id)); e != nil {
				return e
			}
			if e := b.publish(ctx, v); e != nil {
				return e
			}
		}
	}
	for _, snapshot := range b.state.Snapshots {
		if snapshot.Ready && !b.state.SnapshotDeleting[snapshot.Id] {
			if e := b.cmd(ctx, "lvchange", "--activate", "y", "--ignoreactivationskip", device(snapshot.Vg, snapshot.Id)); e != nil {
				return e
			}
		}
	}
	return b.save()
}

// Reap retries physical reclamation after NFS kernel references expire. The
// durable tombstone prevents deleted exports from being resurrected on restart.
func (b *Backend) Reap(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.mu.Lock()
			ids := []string{}
			snapshots := []string{}
			for id, v := range b.state.Volumes {
				if !v.Ready {
					b.state.Deleting[id] = true
				}
			}
			for id, s := range b.state.Snapshots {
				if !s.Ready || b.state.SnapshotDeleting[id] {
					snapshots = append(snapshots, id)
				}
			}
			for id := range b.state.Deleting {
				ids = append(ids, id)
			}
			b.mu.Unlock()
			for _, id := range ids {
				c, cancel := context.WithTimeout(ctx, 10*time.Second)
				if _, err := b.DeleteVolume(c, &pb.ID{Id: id}); err != nil {
					log.Printf("reclaim volume %s: %v", id, err)
				}
				cancel()
			}
			for _, id := range snapshots {
				c, cancel := context.WithTimeout(ctx, 10*time.Second)
				if _, err := b.DeleteSnapshot(c, &pb.ID{Id: id}); err != nil {
					log.Printf("reclaim incomplete snapshot %s: %v", id, err)
				}
				cancel()
			}
		}
	}
}

// AcquireVolume fences iSCSI staging across nodes. Persisting ownership prevents
// an API restart from permitting two independent filesystem writers.
func (b *Backend) AcquireVolume(ctx context.Context, r *pb.VolumeLease) (*pb.Empty, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.VolumeId == "" || r.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume and node required")
	}
	v := b.state.Volumes[r.VolumeId]
	if v == nil || !v.Ready || b.state.Deleting[r.VolumeId] {
		return nil, status.Error(codes.NotFound, "volume not found")
	}
	if v.Protocol != "iscsi" {
		return &pb.Empty{}, nil
	}
	if owner := b.state.Owners[r.VolumeId]; owner != "" && owner != r.NodeId {
		return nil, status.Error(codes.FailedPrecondition, "iSCSI volume is staged on another node")
	}
	b.state.Owners[r.VolumeId] = r.NodeId
	return &pb.Empty{}, internal(b.save())
}
func (b *Backend) ReleaseVolume(ctx context.Context, r *pb.VolumeLease) (*pb.Empty, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.VolumeId == "" || r.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume and node required")
	}
	// Releasing a volume another node now owns changes nothing. This happens when
	// a fenced node comes back and cleans up after its volume was taken over.
	if owner := b.state.Owners[r.VolumeId]; owner != r.NodeId {
		return &pb.Empty{}, nil
	}
	delete(b.state.Owners, r.VolumeId)
	return &pb.Empty{}, internal(b.save())
}

// nfsExportLine is shared by provisioning and startup reconciliation via publish.
func (b *Backend) nfsExportLine(v *pb.Volume) string {
	options := "rw,sync,no_subtree_check,no_root_squash"
	if b.cfg.NFSInsecure {
		options += ",insecure"
	}
	// fsid is numeric, not a hexadecimal token.
	n, _ := strconv.ParseUint(v.Id[2:10], 16, 32)
	return fmt.Sprintf("%s %s(%s,fsid=%d)\n", v.Path, b.cfg.Clients, options, n)
}

func (b *Backend) refreshExports(ctx context.Context) error {
	now := time.Now().Unix()
	if now <= b.lastExport {
		wait := time.Until(time.Unix(b.lastExport+1, 0))
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	err := b.cmd(ctx, "exportfs", "-ra")
	b.lastExport = time.Now().Unix()
	return err
}

func (b *Backend) lvExists(ctx context.Context, vg, id string) (bool, error) {
	out, err := b.run.Run(ctx, "lvs", "--noheadings", "-o", "lv_name", "--select", "lv_name="+id, vg)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == id, nil
}
