package backend

import (
	"context"
	"errors"
	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
)

type recordingRunner struct {
	calls      []string
	failCreate bool
	onRun      func(string)
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
	return nil, nil
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
	if _, e = b.ReleaseVolume(ctx, &pb.VolumeLease{VolumeId: v.Id, NodeId: "node-b"}); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("wrong owner released volume: %v", e)
	}
	if _, e = b.AcquireVolume(ctx, a); e != nil {
		t.Fatal(e)
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
