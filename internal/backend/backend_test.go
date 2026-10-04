package backend

import (
	"context"
	"errors"
	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
	"time"
)

type recordingRunner struct {
	calls      []string
	failCreate bool
	onRun      func(string)
	acls       map[string]bool // initiators present on targets, as targetcli would list them
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if r.onRun != nil {
		r.onRun(call)
	}
	if name == "lvcreate" && r.failCreate {
		return nil, errors.New("interrupted create")
	}
	if strings.Contains(call, "segtype") {
		return []byte("thin-pool"), nil
	}
	if strings.Contains(call, "lv_attr") {
		return []byte("Vwi-a-tz--"), nil
	}
	if name == "targetcli" && len(args) >= 2 && strings.HasSuffix(args[0], "/acls") {
		if r.acls == nil {
			r.acls = map[string]bool{}
		}
		switch args[1] {
		case "create":
			r.acls[args[2]] = true
		case "delete":
			delete(r.acls, args[2])
		case "ls":
			out := "o- acls\n"
			for initiator := range r.acls {
				out += "  o- " + initiator + " ...... [Mapped LUNs: 1]\n    o- mapped_lun0\n"
			}
			return []byte(out), nil
		}
	}
	return nil, nil
}
func (r *recordingRunner) last(prefix string) string {
	for i := len(r.calls) - 1; i >= 0; i-- {
		if strings.HasPrefix(r.calls[i], prefix) {
			return r.calls[i]
		}
	}
	return ""
}
func testBackend(t *testing.T, r Runner) *Backend {
	t.Helper()
	b, e := New(Config{Root: t.TempDir(), Server: "10.0.0.1", VGs: []string{"vg"}}, r)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestInterruptedCreateIsReclaimedBeforeRetry(t *testing.T) {
	r := &recordingRunner{failCreate: true}
	b := testBackend(t, r)
	ctx := context.Background()
	req := &pb.CreateVolumeRequest{Name: "interrupted", Bytes: 128 << 20, Protocol: "iscsi", Block: true}
	if _, e := b.CreateVolume(ctx, req); e == nil {
		t.Fatal("expected injected failure")
	}
	restarted, e := New(b.cfg, r)
	if e != nil {
		t.Fatal(e)
	}
	if e = restarted.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	id := ID("v-", req.Name)
	if !restarted.state.Deleting[id] {
		t.Fatal("missing durable reclamation intent")
	}
	if _, e = restarted.GetVolume(ctx, &pb.ID{Id: id}); status.Code(e) != codes.NotFound {
		t.Fatalf("incomplete volume visible: %v", e)
	}
	if _, e = restarted.DeleteVolume(ctx, &pb.ID{Id: id}); e != nil {
		t.Fatal(e)
	}
	r.failCreate = false
	if _, e = restarted.CreateVolume(ctx, req); e != nil {
		t.Fatal(e)
	}
}
func TestRawClonePreservesFilesystemAndDoesNotFormat(t *testing.T) {
	r := &recordingRunner{}
	b := testBackend(t, r)
	ctx := context.Background()
	b.state.Snapshots["snapshot"] = &pb.Snapshot{Id: "snapshot", Vg: "vg", Bytes: 128 << 20, Filesystem: "ext4", Ready: true}
	req := &pb.CreateVolumeRequest{Name: "raw-clone", Bytes: 128 << 20, Protocol: "iscsi", Block: true, SourceSnapshot: "snapshot"}
	v, e := b.CreateVolume(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	if !v.Block || v.Filesystem != "ext4" {
		t.Fatal("raw clone lost source filesystem information")
	}
	for _, call := range r.calls {
		if strings.HasPrefix(call, "mkfs.") || strings.HasPrefix(call, "mount ") {
			t.Fatalf("raw snapshot clone modified: %s", call)
		}
	}
	count := len(r.calls)
	if _, e = b.CreateVolume(ctx, req); e != nil {
		t.Fatal(e)
	}
	if len(r.calls) != count {
		t.Fatal("idempotent create repeated storage operations")
	}
	req.Bytes = 256 << 20
	if _, e = b.CreateVolume(ctx, req); status.Code(e) != codes.AlreadyExists {
		t.Fatalf("incompatible retry accepted: %v", e)
	}
}

func TestISCSIOwnershipSurvivesRestart(t *testing.T) {
	r := &recordingRunner{}
	b := testBackend(t, r)
	ctx := context.Background()
	v, e := b.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "owned", Bytes: 128 << 20, Protocol: "iscsi", Block: true})
	if e != nil {
		t.Fatal(e)
	}
	a := &pb.VolumeLease{VolumeId: v.Id, NodeId: "node-a"}
	if _, e = b.AcquireVolume(ctx, a); e != nil {
		t.Fatal(e)
	}
	b, e = New(b.cfg, r)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.AcquireVolume(ctx, &pb.VolumeLease{VolumeId: v.Id, NodeId: "node-b"}); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("second node acquired volume: %v", e)
	}
	if _, e = b.DeleteVolume(ctx, &pb.ID{Id: v.Id}); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("staged volume deleted: %v", e)
	}
	// A node that does not own the volume may clean up, but cannot release it.
	if _, e = b.ReleaseVolume(ctx, &pb.VolumeLease{VolumeId: v.Id, NodeId: "node-b"}); e != nil {
		t.Fatalf("non-owner release failed: %v", e)
	}
	if _, e = b.AcquireVolume(ctx, &pb.VolumeLease{VolumeId: v.Id, NodeId: "node-b"}); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("non-owner release removed ownership: %v", e)
	}
	if _, e = b.ReleaseVolume(ctx, a); e != nil {
		t.Fatal(e)
	}
	if _, e = b.DeleteVolume(ctx, &pb.ID{Id: v.Id}); e != nil {
		t.Fatal(e)
	}
}

func TestNewBlockIgnoresStorageClassFilesystem(t *testing.T) {
	r := &recordingRunner{}
	b := testBackend(t, r)
	v, err := b.CreateVolume(context.Background(), &pb.CreateVolumeRequest{Name: "blank-block", Bytes: 128 << 20, Protocol: "iscsi", Block: true, Filesystem: "ext4"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Filesystem != "" {
		t.Fatal("new raw device must be unformatted")
	}
	for _, call := range r.calls {
		if strings.HasPrefix(call, "mkfs.") || strings.HasPrefix(call, "mount ") {
			t.Fatalf("raw device modified: %s", call)
		}
	}
}

func TestFailedReadyPersistenceCannotAcknowledgeRetry(t *testing.T) {
	r := &recordingRunner{}
	b := testBackend(t, r)
	root := b.cfg.Root
	r.onRun = func(call string) {
		if call == "targetcli saveconfig" {
			b.cfg.Root = root + "/missing"
		}
	}
	req := &pb.CreateVolumeRequest{Name: "persistence-failure", Bytes: 128 << 20, Protocol: "iscsi", Block: true}
	if _, err := b.CreateVolume(context.Background(), req); err == nil {
		t.Fatal("expected state write failure")
	}
	b.cfg.Root = root
	r.onRun = nil
	if _, err := b.GetVolume(context.Background(), &pb.ID{Id: ID("v-", req.Name)}); status.Code(err) != codes.NotFound {
		t.Fatalf("uncommitted volume advertised ready: %v", err)
	}
	if _, err := b.CreateVolume(context.Background(), req); status.Code(err) != codes.Aborted {
		t.Fatalf("uncommitted retry acknowledged: %v", err)
	}
}

func TestNFSExportSourcePortPolicy(t *testing.T) {
	v := &pb.Volume{Id: "v-12345678abcdef", Path: "/var/lib/lvmo/volumes/v-12345678abcdef"}
	b := testBackend(t, &recordingRunner{})
	b.cfg.Clients = "10.0.0.0/24"
	secure := "/var/lib/lvmo/volumes/v-12345678abcdef 10.0.0.0/24(rw,sync,no_subtree_check,no_root_squash,fsid=305419896)\n"
	if got := b.nfsExportLine(v); got != secure {
		t.Fatalf("default export policy changed: %q", got)
	}
	b.cfg.NFSInsecure = true
	insecure := strings.Replace(secure, ",fsid=", ",insecure,fsid=", 1)
	if got := b.nfsExportLine(v); got != insecure {
		t.Fatalf("opt-in export missing source-port allowance: %q", got)
	}
	// A restarted API uses its current flag, not the prior generated export.
	restarted, err := New(b.cfg, &recordingRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.nfsExportLine(v); got != insecure {
		t.Fatalf("restart lost opt-in: %q", got)
	}
	restarted.cfg.NFSInsecure = false
	if got := restarted.nfsExportLine(v); got != secure {
		t.Fatalf("disabling flag did not restore default: %q", got)
	}
}

// A confirmed node failure ends in ControllerUnpublishVolume for the dead node:
// it must be cut off at the target and its ownership released, so that another
// node can attach and stage the volume, while a third node stays refused.
func TestUnpublishFencesNodeAndAllowsTakeover(t *testing.T) {
	r := &recordingRunner{}
	b := testBackend(t, r)
	ctx := context.Background()
	v, e := b.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "takeover", Bytes: 128 << 20, Protocol: "iscsi"})
	if e != nil {
		t.Fatal(e)
	}
	tpg := "targetcli /iscsi/" + v.Iqn + "/tpg1 set attribute"
	if !strings.Contains(r.last(tpg), "generate_node_acls=1") {
		t.Fatal("unpublished target should stay open for direct staging")
	}
	a := &pb.VolumePublish{VolumeId: v.Id, NodeId: "lvmo:a:iqn.2004-10.com.ubuntu:01:a", Initiator: "iqn.2004-10.com.ubuntu:01:a"}
	bNode := &pb.VolumePublish{VolumeId: v.Id, NodeId: "lvmo:b:iqn.2004-10.com.ubuntu:01:b", Initiator: "iqn.2004-10.com.ubuntu:01:b"}
	if _, e = b.PublishVolume(ctx, a); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(r.last(tpg), "generate_node_acls=0") || !r.acls[a.Initiator] || len(r.acls) != 1 {
		t.Fatalf("published target must admit only node A: %v %v", r.last(tpg), r.acls)
	}
	if _, e = b.PublishVolume(ctx, a); e != nil {
		t.Fatalf("publish must be idempotent: %v", e)
	}
	if _, e = b.PublishVolume(ctx, bNode); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("second node published while first attached: %v", e)
	}
	if _, e = b.AcquireVolume(ctx, &pb.VolumeLease{VolumeId: v.Id, NodeId: a.NodeId}); e != nil {
		t.Fatal(e)
	}
	if _, e = b.UnpublishVolume(ctx, &pb.VolumePublish{VolumeId: v.Id, NodeId: a.NodeId}); e != nil {
		t.Fatal(e)
	}
	if len(r.acls) != 0 || r.last("targetcli /iscsi/"+v.Iqn+"/tpg1/acls delete") == "" {
		t.Fatalf("node A not fenced: %v", r.acls)
	}
	if _, e = b.PublishVolume(ctx, bNode); e != nil {
		t.Fatal(e)
	}
	if _, e = b.AcquireVolume(ctx, &pb.VolumeLease{VolumeId: v.Id, NodeId: bNode.NodeId}); e != nil {
		t.Fatalf("takeover could not stage: %v", e)
	}
	if !r.acls[bNode.Initiator] || r.acls[a.Initiator] {
		t.Fatalf("only node B should be admitted: %v", r.acls)
	}
	// Node A comes back and cleans up: that must not release node B's volume.
	if _, e = b.ReleaseVolume(ctx, &pb.VolumeLease{VolumeId: v.Id, NodeId: a.NodeId}); e != nil {
		t.Fatal(e)
	}
	if _, e = b.DeleteVolume(ctx, &pb.ID{Id: v.Id}); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("attached volume deleted: %v", e)
	}
}

// After the last node is unpublished the target must stay closed, also across
// an API restart, so that a fenced node cannot log in again.
func TestFencedTargetStaysClosedAfterRestart(t *testing.T) {
	r := &recordingRunner{}
	b := testBackend(t, r)
	ctx := context.Background()
	v, e := b.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "fenced", Bytes: 128 << 20, Protocol: "iscsi"})
	if e != nil {
		t.Fatal(e)
	}
	a := &pb.VolumePublish{VolumeId: v.Id, NodeId: "lvmo:a:iqn.2004-10.com.ubuntu:01:a", Initiator: "iqn.2004-10.com.ubuntu:01:a"}
	if _, e = b.PublishVolume(ctx, a); e != nil {
		t.Fatal(e)
	}
	if _, e = b.UnpublishVolume(ctx, &pb.VolumePublish{VolumeId: v.Id, NodeId: a.NodeId}); e != nil {
		t.Fatal(e)
	}
	// A stale ACL left by an interrupted unpublish must be removed on restart.
	r.acls[a.Initiator] = true
	restarted, e := New(b.cfg, r)
	if e != nil {
		t.Fatal(e)
	}
	if e = restarted.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if got := r.last("targetcli /iscsi/" + v.Iqn + "/tpg1 set attribute"); !strings.Contains(got, "generate_node_acls=0") {
		t.Fatalf("restart reopened a fenced target: %s", got)
	}
	if len(r.acls) != 0 {
		t.Fatalf("stale ACL survived restart: %v", r.acls)
	}
	if _, e = restarted.DeleteVolume(ctx, &pb.ID{Id: v.Id}); e != nil {
		t.Fatal(e)
	}
}

func TestPublishValidation(t *testing.T) {
	b := testBackend(t, &recordingRunner{})
	ctx := context.Background()
	if _, e := b.PublishVolume(ctx, &pb.VolumePublish{VolumeId: "missing", NodeId: "lvmo:a:", Initiator: ""}); status.Code(e) != codes.NotFound {
		t.Fatalf("missing volume: %v", e)
	}
	b.state.Volumes["nfs"] = &pb.Volume{Id: "nfs", Protocol: "nfs", Ready: true}
	if _, e := b.PublishVolume(ctx, &pb.VolumePublish{VolumeId: "nfs", NodeId: "lvmo:a:"}); e != nil {
		t.Fatalf("NFS publish needs no initiator: %v", e)
	}
	iscsi, e := b.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "iscsi", Bytes: 128 << 20, Protocol: "iscsi"})
	if e != nil {
		t.Fatal(e)
	}
	for _, initiator := range []string{"", "not-an-iqn", "iqn.2004-10.com.ubuntu:01:a b"} {
		if _, e = b.PublishVolume(ctx, &pb.VolumePublish{VolumeId: iscsi.Id, NodeId: "lvmo:a:x", Initiator: initiator}); status.Code(e) != codes.FailedPrecondition {
			t.Fatalf("initiator %q accepted: %v", initiator, e)
		}
	}
}

// A storage server only fences a node it has not heard from for the requested
// silence, counting from its own start for nodes it never heard from.
func TestFenceNodeRequiresSilence(t *testing.T) {
	r := &recordingRunner{}
	b := testBackend(t, r)
	clock := b.started
	b.now = func() time.Time { return clock }
	ctx := context.Background()
	v, e := b.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "fence", Bytes: 128 << 20, Protocol: "iscsi"})
	if e != nil {
		t.Fatal(e)
	}
	a := &pb.VolumePublish{VolumeId: v.Id, NodeId: "lvmo:a:iqn.2004-10.com.ubuntu:01:a", Initiator: "iqn.2004-10.com.ubuntu:01:a"}
	if _, e = b.PublishVolume(ctx, a); e != nil {
		t.Fatal(e)
	}
	if _, e = b.AcquireVolume(ctx, &pb.VolumeLease{VolumeId: v.Id, NodeId: a.NodeId}); e != nil {
		t.Fatal(e)
	}
	fence := &pb.NodeFence{NodeId: a.NodeId, SilenceSeconds: 120}
	clock = clock.Add(time.Minute)
	if _, e = b.FenceNode(ctx, fence); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("fenced within the grace period after start: %v", e)
	}
	clock = clock.Add(2 * time.Minute)
	if _, e = b.Heartbeat(ctx, &pb.NodeHeartbeat{NodeId: a.NodeId}); e != nil {
		t.Fatal(e)
	}
	clock = clock.Add(119 * time.Second)
	if _, e = b.FenceNode(ctx, fence); status.Code(e) != codes.FailedPrecondition || !r.acls[a.Initiator] {
		t.Fatalf("fenced a node heard 119s ago: %v", e)
	}
	clock = clock.Add(time.Second)
	fenced, e := b.FenceNode(ctx, fence)
	if e != nil || len(fenced.VolumeIds) != 1 || fenced.VolumeIds[0] != v.Id {
		t.Fatalf("silent node not fenced: %v %v", fenced, e)
	}
	if r.acls[a.Initiator] || b.state.Owners[v.Id] != "" {
		t.Fatalf("fenced node kept access or ownership: %v %q", r.acls, b.state.Owners[v.Id])
	}
	if fenced, e = b.FenceNode(ctx, fence); e != nil || len(fenced.VolumeIds) != 0 {
		t.Fatalf("second fence should find nothing: %v %v", fenced, e)
	}
	// Fencing by node name finds the node's IDs without knowing its initiator.
	if _, e = b.PublishVolume(ctx, a); e != nil {
		t.Fatal(e)
	}
	if _, e = b.FenceNode(ctx, &pb.NodeFence{NodeName: "a", SilenceSeconds: 120, NodeId: a.NodeId}); status.Code(e) != codes.InvalidArgument {
		t.Fatalf("node ID and name together must be rejected: %v", e)
	}
	if fenced, e = b.FenceNode(ctx, &pb.NodeFence{NodeName: "a", SilenceSeconds: 120}); e != nil || len(fenced.VolumeIds) != 1 || r.acls[a.Initiator] {
		t.Fatalf("fence by name failed: %v %v %v", fenced, e, r.acls)
	}
}
