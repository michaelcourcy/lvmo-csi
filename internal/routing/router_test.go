package routing

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"github.com/michaelcourcy/lvmo-csi/internal/backend"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type testAPI struct {
	pb.UnimplementedStorageServer
	mu        sync.Mutex
	volumes   map[string]*pb.Volume
	snapshots map[string]*pb.Snapshot
	lastLease string
}

func startAPI(t *testing.T) (string, *testAPI) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api := &testAPI{volumes: map[string]*pb.Volume{}, snapshots: map[string]*pb.Snapshot{}}
	server := grpc.NewServer()
	pb.RegisterStorageServer(server, api)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	return listener.Addr().String(), api
}
func (a *testAPI) CreateVolume(_ context.Context, r *pb.CreateVolumeRequest) (*pb.Volume, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := backend.ID("v-", r.Name)
	v := &pb.Volume{Id: id, Name: r.Name, Bytes: r.Bytes, Protocol: r.Protocol, SourceSnapshot: r.SourceSnapshot, SourceVolume: r.SourceVolume, Ready: true}
	a.volumes[id] = v
	return proto.Clone(v).(*pb.Volume), nil
}
func (a *testAPI) GetVolume(_ context.Context, r *pb.ID) (*pb.Volume, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.volumes[r.Id]
	if v == nil {
		return nil, status.Error(codes.NotFound, "volume")
	}
	return proto.Clone(v).(*pb.Volume), nil
}
func (a *testAPI) DeleteVolume(_ context.Context, r *pb.ID) (*pb.Empty, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.volumes, r.Id)
	return &pb.Empty{}, nil
}
func (a *testAPI) ExpandVolume(_ context.Context, r *pb.ExpandRequest) (*pb.Volume, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.volumes[r.Id]
	if v == nil {
		return nil, status.Error(codes.NotFound, "volume")
	}
	v.Bytes = r.Bytes
	return proto.Clone(v).(*pb.Volume), nil
}
func (a *testAPI) ListVolumes(context.Context, *pb.Empty) (*pb.Volumes, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := &pb.Volumes{}
	for _, v := range a.volumes {
		out.Volumes = append(out.Volumes, proto.Clone(v).(*pb.Volume))
	}
	return out, nil
}
func (a *testAPI) CreateSnapshot(_ context.Context, r *pb.SnapshotRequest) (*pb.Snapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.volumes[r.VolumeId] == nil {
		return nil, status.Error(codes.NotFound, "source")
	}
	s := &pb.Snapshot{Id: backend.ID("s-", r.Name), VolumeId: r.VolumeId, Ready: true}
	a.snapshots[s.Id] = s
	return proto.Clone(s).(*pb.Snapshot), nil
}
func (a *testAPI) ListSnapshots(context.Context, *pb.Empty) (*pb.Snapshots, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := &pb.Snapshots{}
	for _, s := range a.snapshots {
		out.Snapshots = append(out.Snapshots, proto.Clone(s).(*pb.Snapshot))
	}
	return out, nil
}
func (a *testAPI) DeleteSnapshot(_ context.Context, r *pb.ID) (*pb.Empty, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.snapshots, r.Id)
	return &pb.Empty{}, nil
}
func (a *testAPI) GetCapacity(context.Context, *pb.CapacityRequest) (*pb.Capacity, error) {
	return &pb.Capacity{AvailableBytes: 4096}, nil
}
func (a *testAPI) AcquireVolume(_ context.Context, r *pb.VolumeLease) (*pb.Empty, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.volumes[r.VolumeId] == nil {
		return nil, status.Error(codes.NotFound, "volume")
	}
	a.lastLease = r.VolumeId
	return &pb.Empty{}, nil
}
func (a *testAPI) ReleaseVolume(_ context.Context, r *pb.VolumeLease) (*pb.Empty, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastLease != r.VolumeId {
		return nil, status.Error(codes.InvalidArgument, "wrong lease")
	}
	a.lastLease = ""
	return &pb.Empty{}, nil
}
func (a *testAPI) Metadata(r *pb.MetadataRequest, s grpc.ServerStreamingServer[pb.Ranges]) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.snapshots[r.Snapshot] == nil {
		return status.Error(codes.NotFound, "snapshot")
	}
	if r.BaseSnapshot != "" && a.snapshots[r.BaseSnapshot] == nil {
		return status.Error(codes.NotFound, "base")
	}
	return s.Send(&pb.Ranges{Capacity: 4096, Ranges: []*pb.Range{{Offset: 0, Length: 4096}}})
}

func TestRoutingSurvivesRestartAndSeparatesBackends(t *testing.T) {
	a, apiA := startAPI(t)
	b, _ := startAPI(t)
	discover := func(context.Context) ([]string, error) { return []string{a, b}, nil }
	router, err := New("", discover)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	create := func(endpoint string) *pb.Volume {
		t.Helper()
		v, err := router.CreateVolume(WithEndpoint(ctx, endpoint), &pb.CreateVolumeRequest{Name: "same-backend-name", Bytes: 4096, Protocol: "iscsi"})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	va, vb := create(a), create(b)
	if va.Id == vb.Id {
		t.Fatal("backend-local IDs collided")
	}
	sa, err := router.CreateSnapshot(ctx, &pb.SnapshotRequest{Name: "base", VolumeId: va.Id})
	if err != nil {
		t.Fatal(err)
	}
	sb, err := router.CreateSnapshot(ctx, &pb.SnapshotRequest{Name: "base", VolumeId: vb.Id})
	if err != nil {
		t.Fatal(err)
	}
	router.Close()
	// A fresh process has no remembered connections, and no default endpoint.
	router, err = New("", discover)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	for _, v := range []*pb.Volume{va, vb} {
		got, err := router.GetVolume(ctx, &pb.ID{Id: v.Id})
		if err != nil || got.Id != v.Id {
			t.Fatalf("restart lost routing: %v", err)
		}
	}
	volumes, err := router.ListVolumes(ctx, &pb.Empty{})
	if err != nil || len(volumes.Volumes) != 2 {
		t.Fatalf("discovery lost volumes: %v", err)
	}
	snapshots, err := router.ListSnapshots(ctx, &pb.Empty{})
	if err != nil || len(snapshots.Snapshots) != 2 {
		t.Fatalf("discovery lost snapshots: %v", err)
	}
	targeted, err := router.ListSnapshots(WithHandle(ctx, sb.Id), &pb.Empty{})
	if err != nil || len(targeted.Snapshots) != 1 || targeted.Snapshots[0].Id != sb.Id {
		t.Fatalf("filtered listing crossed backends: %v", err)
	}
	capacity, err := router.GetCapacity(WithEndpoint(ctx, b), &pb.CapacityRequest{})
	if err != nil || capacity.AvailableBytes != 4096 {
		t.Fatalf("wrong targeted capacity: %v", err)
	}
	capacity, err = router.GetCapacity(ctx, &pb.CapacityRequest{})
	if err != nil || capacity.AvailableBytes != 8192 {
		t.Fatalf("wrong aggregate capacity: %v", err)
	}
	expanded, err := router.ExpandVolume(ctx, &pb.ExpandRequest{Id: vb.Id, Bytes: 8192})
	if err != nil || expanded.Bytes != 8192 {
		t.Fatalf("expand failed: %v", err)
	}
	untouched, err := router.GetVolume(ctx, &pb.ID{Id: va.Id})
	if err != nil || untouched.Bytes != 4096 {
		t.Fatalf("expand touched another backend: %v", err)
	}
	lease := &pb.VolumeLease{VolumeId: va.Id, NodeId: "node"}
	if _, err = router.AcquireVolume(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err = router.ReleaseVolume(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err = router.CreateVolume(WithEndpoint(ctx, b), &pb.CreateVolumeRequest{Name: "invalid", SourceSnapshot: sa.Id}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("cross-backend restore accepted: %v", err)
	}
	clone, err := router.CreateVolume(WithEndpoint(ctx, a), &pb.CreateVolumeRequest{Name: "clone", Protocol: "iscsi", Block: true, SourceSnapshot: sa.Id})
	if err != nil || clone.SourceSnapshot != sa.Id {
		t.Fatalf("same-backend restore failed: %v", err)
	}
	stream, err := router.Metadata(ctx, &pb.MetadataRequest{Snapshot: sa.Id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); err != io.EOF {
		t.Fatalf("stream not complete: %v", err)
	}
	if _, err = router.Metadata(ctx, &pb.MetadataRequest{Snapshot: sa.Id, BaseSnapshot: sb.Id}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("cross-backend delta accepted: %v", err)
	}
	if _, err = router.DeleteVolume(ctx, &pb.ID{Id: vb.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err = router.GetVolume(ctx, &pb.ID{Id: va.Id}); err != nil {
		t.Fatal("deleting B affected A", err)
	}
	if _, err = router.DeleteSnapshot(ctx, &pb.ID{Id: sb.Id}); err != nil {
		t.Fatal(err)
	}
	apiA.mu.Lock()
	defer apiA.mu.Unlock()
	if len(apiA.snapshots) != 1 {
		t.Fatal("deleting B snapshot affected A")
	}
}

func TestDiscoveryFromDurableResources(t *testing.T) {
	a, b, c := "one.example:50051", "two.example:50051", "three.example:50051"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rotated" {
			t.Error("missing service-account token")
		}
		switch r.URL.Path {
		case "/apis/storage.k8s.io/v1/storageclasses":
			io.WriteString(w, `{"items":[{"provisioner":"lvmo.csi.io","parameters":{"endpoint":"`+a+`"}},{"provisioner":"other","parameters":{"endpoint":"ignored:1"}}]}`)
		case "/api/v1/persistentvolumes":
			io.WriteString(w, `{"items":[{"spec":{"csi":{"driver":"lvmo.csi.io","volumeHandle":"`+Encode(b, "v-id")+`"}}}]}`)
		default:
			io.WriteString(w, `{"items":[{"spec":{"driver":"lvmo.csi.io","source":{"snapshotHandle":"`+Encode(c, "s-id")+`"}}}]}`)
		}
	}))
	defer server.Close()
	discover := discoverResources(server.Client(), server.URL, "lvmo.csi.io", func() ([]byte, error) { return []byte("rotated"), nil })
	endpoints, err := discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(endpoints, ",") != strings.Join([]string{a, b, c}, ",") {
		t.Fatalf("lost durable endpoints: %v", endpoints)
	}
}
func TestEndpointValidation(t *testing.T) {
	for _, address := range []string{"", "https://host:123", "host", "host:0", "host:70000", "user@host:50051"} {
		if _, err := NormalizeEndpoint(address); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	for _, address := range []string{"host:50051", "127.0.0.1:50051", "[::1]:50051"} {
		normalized, err := NormalizeEndpoint(address)
		if err != nil {
			t.Fatal(err)
		}
		got, id, err := Decode(Encode(normalized, "v-123"))
		if err != nil || got != normalized || id != "v-123" {
			t.Fatalf("handle round trip: %v", err)
		}
	}
}

func TestRemovedBackendIsNotKeptAliveByConnectionCache(t *testing.T) {
	a, _ := startAPI(t)
	b, _ := startAPI(t)
	registered := []string{a, b}
	router, err := New("", func(context.Context) ([]string, error) { return registered, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	ctx := context.Background()
	for _, endpoint := range registered {
		if _, err := router.CreateVolume(WithEndpoint(ctx, endpoint), &pb.CreateVolumeRequest{Name: "cached", Bytes: 4096}); err != nil {
			t.Fatal(err)
		}
	}
	registered = []string{a}
	volumes, err := router.ListVolumes(ctx, &pb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes.Volumes) != 1 {
		t.Fatal("cached connection resurrected a removed backend")
	}
}

func TestCredentialsAreChosenPerEndpoint(t *testing.T) {
	router, err := New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	asked := []string{}
	router.Credentials = func(endpoint string) (credentials.TransportCredentials, error) {
		asked = append(asked, endpoint)
		return nil, io.ErrUnexpectedEOF
	}
	_, err = router.CreateVolume(WithEndpoint(context.Background(), "Storage.Example:50051"), &pb.CreateVolumeRequest{Name: "tls", Bytes: 4096})
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "storage.example:50051") {
		t.Fatalf("unexpected error %v", err)
	}
	if len(asked) != 1 || asked[0] != "storage.example:50051" {
		t.Fatalf("credentials asked for %v", asked)
	}
}
