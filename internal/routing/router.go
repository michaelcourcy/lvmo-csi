// Package routing keeps backend selection in opaque CSI handles rather than in
// process-local state. The management servers continue to use their own IDs.
package routing

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"

	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const handlePrefix = "lvmo1."

type contextKey int

const (
	endpointKey contextKey = iota
	handleKey
)

func WithEndpoint(ctx context.Context, endpoint string) context.Context {
	return context.WithValue(ctx, endpointKey, endpoint)
}
func WithHandle(ctx context.Context, handle string) context.Context {
	return context.WithValue(ctx, handleKey, handle)
}

// Discover enumerates backends from durable cluster resources, including
// retained snapshots whose original PVC and StorageClass no longer exist.
type Discover func(context.Context) ([]string, error)
type Router struct {
	fallback    string
	discover    Discover
	mu          sync.Mutex
	clients     map[string]pb.StorageClient
	connections map[string]*grpc.ClientConn
}

var _ pb.StorageClient = (*Router)(nil)

func New(fallback string, discover Discover) (*Router, error) {
	var err error
	if fallback != "" {
		fallback, err = NormalizeEndpoint(fallback)
		if err != nil {
			return nil, err
		}
	}
	return &Router{fallback: fallback, discover: discover, clients: map[string]pb.StorageClient{}, connections: map[string]*grpc.ClientConn{}}, nil
}
func NormalizeEndpoint(endpoint string) (string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" || strings.ContainsAny(host, "/\\ \t\r\n@?#") {
		return "", status.Error(codes.InvalidArgument, "endpoint must be a host:port API address")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", status.Error(codes.InvalidArgument, "invalid API port")
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		host = strings.ToLower(host)
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}
func Encode(endpoint, id string) string {
	if id == "" {
		return ""
	}
	return handlePrefix + base64.RawURLEncoding.EncodeToString([]byte(endpoint)) + "." + id
}
func Decode(handle string) (string, string, error) {
	if !strings.HasPrefix(handle, handlePrefix) {
		return "", handle, nil
	}
	parts := strings.Split(strings.TrimPrefix(handle, handlePrefix), ".")
	if len(parts) != 2 || parts[1] == "" || strings.ContainsAny(parts[1], "/\\ \t\r\n") {
		return "", "", status.Error(codes.InvalidArgument, "invalid routed handle")
	}
	address, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, "invalid routed endpoint")
	}
	endpoint, err := NormalizeEndpoint(string(address))
	if err != nil {
		return "", "", err
	}
	return endpoint, parts[1], nil
}
func (r *Router) route(handle string) (string, string, error) {
	if handle == "" {
		return "", "", status.Error(codes.InvalidArgument, "handle required")
	}
	endpoint, id, err := Decode(handle)
	if err != nil {
		return "", "", err
	}
	if endpoint == "" {
		endpoint = r.fallback
	}
	if endpoint == "" {
		return "", "", status.Error(codes.NotFound, "unqualified handle has no default backend")
	}
	return endpoint, id, nil
}
func (r *Router) selected(ctx context.Context) (string, error) {
	endpoint, _ := ctx.Value(endpointKey).(string)
	if endpoint == "" {
		endpoint = r.fallback
	}
	if endpoint == "" {
		return "", status.Error(codes.InvalidArgument, "StorageClass parameter endpoint is required")
	}
	return NormalizeEndpoint(endpoint)
}
func (r *Router) client(endpoint string) (pb.StorageClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.clients[endpoint]; c != nil {
		return c, nil
	}
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	c := pb.NewStorageClient(conn)
	r.connections[endpoint] = conn
	r.clients[endpoint] = c
	return c, nil
}
func (r *Router) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.connections {
		_ = c.Close()
	}
}
func (r *Router) endpoints(ctx context.Context) ([]string, error) {
	if handle, _ := ctx.Value(handleKey).(string); handle != "" {
		endpoint, _, err := r.route(handle)
		if err != nil {
			return nil, err
		}
		return []string{endpoint}, nil
	}
	values := []string{}
	if r.fallback != "" {
		values = append(values, r.fallback)
	}
	// In Kubernetes, durable resources are authoritative. A cached connection
	// must not keep a removed StorageClass/backend in every future list request.
	if r.discover == nil {
		r.mu.Lock()
		for endpoint := range r.clients {
			values = append(values, endpoint)
		}
		r.mu.Unlock()
	}
	if r.discover != nil {
		found, err := r.discover(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "discover storage backends: %v", err)
		}
		values = append(values, found...)
	}
	unique := map[string]bool{}
	for _, value := range values {
		endpoint, err := NormalizeEndpoint(value)
		if err != nil {
			return nil, err
		}
		unique[endpoint] = true
	}
	out := make([]string, 0, len(unique))
	for endpoint := range unique {
		out = append(out, endpoint)
	}
	sort.Strings(out)
	return out, nil
}
func wrapVolume(endpoint string, v *pb.Volume) *pb.Volume {
	if v == nil {
		return nil
	}
	v = proto.Clone(v).(*pb.Volume)
	v.Id = Encode(endpoint, v.Id)
	v.SourceVolume = Encode(endpoint, v.SourceVolume)
	v.SourceSnapshot = Encode(endpoint, v.SourceSnapshot)
	v.Lineage = Encode(endpoint, v.Lineage)
	return v
}
func wrapSnapshot(endpoint string, s *pb.Snapshot) *pb.Snapshot {
	if s == nil {
		return nil
	}
	s = proto.Clone(s).(*pb.Snapshot)
	s.Id = Encode(endpoint, s.Id)
	s.VolumeId = Encode(endpoint, s.VolumeId)
	s.Lineage = Encode(endpoint, s.Lineage)
	return s
}
func (r *Router) source(endpoint, handle string) (string, error) {
	if handle == "" {
		return "", nil
	}
	sourceEndpoint, id, err := r.route(handle)
	if err != nil {
		return "", err
	}
	if sourceEndpoint != endpoint {
		return "", status.Error(codes.InvalidArgument, "clones and snapshot restores must use the source backend endpoint")
	}
	return id, nil
}
func (r *Router) CreateVolume(ctx context.Context, request *pb.CreateVolumeRequest, opts ...grpc.CallOption) (*pb.Volume, error) {
	endpoint, err := r.selected(ctx)
	if err != nil {
		return nil, err
	}
	request = proto.Clone(request).(*pb.CreateVolumeRequest)
	request.SourceSnapshot, err = r.source(endpoint, request.SourceSnapshot)
	if err != nil {
		return nil, err
	}
	request.SourceVolume, err = r.source(endpoint, request.SourceVolume)
	if err != nil {
		return nil, err
	}
	c, err := r.client(endpoint)
	if err != nil {
		return nil, err
	}
	v, err := c.CreateVolume(ctx, request, opts...)
	return wrapVolume(endpoint, v), err
}
func (r *Router) GetVolume(ctx context.Context, request *pb.ID, opts ...grpc.CallOption) (*pb.Volume, error) {
	endpoint, id, err := r.route(request.Id)
	if err != nil {
		return nil, err
	}
	c, err := r.client(endpoint)
	if err != nil {
		return nil, err
	}
	v, err := c.GetVolume(ctx, &pb.ID{Id: id}, opts...)
	v = wrapVolume(endpoint, v)
	// Existing unqualified PV handles can still be mounted with the legacy default.
	if v != nil && !strings.HasPrefix(request.Id, handlePrefix) {
		v.Id = request.Id
	}
	return v, err
}
func (r *Router) DeleteVolume(ctx context.Context, request *pb.ID, opts ...grpc.CallOption) (*pb.Empty, error) {
	endpoint, id, err := r.route(request.Id)
	if status.Code(err) == codes.NotFound {
		return &pb.Empty{}, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := r.client(endpoint)
	if err != nil {
		return nil, err
	}
	return c.DeleteVolume(ctx, &pb.ID{Id: id}, opts...)
}
func (r *Router) ExpandVolume(ctx context.Context, request *pb.ExpandRequest, opts ...grpc.CallOption) (*pb.Volume, error) {
	endpoint, id, err := r.route(request.Id)
	if err != nil {
		return nil, err
	}
	c, err := r.client(endpoint)
	if err != nil {
		return nil, err
	}
	v, err := c.ExpandVolume(ctx, &pb.ExpandRequest{Id: id, Bytes: request.Bytes}, opts...)
	return wrapVolume(endpoint, v), err
}
func (r *Router) ListVolumes(ctx context.Context, request *pb.Empty, opts ...grpc.CallOption) (*pb.Volumes, error) {
	endpoints, err := r.endpoints(ctx)
	if err != nil {
		return nil, err
	}
	out := &pb.Volumes{}
	for _, endpoint := range endpoints {
		c, err := r.client(endpoint)
		if err != nil {
			return nil, err
		}
		vs, err := c.ListVolumes(ctx, request, opts...)
		if err != nil {
			return nil, err
		}
		for _, v := range vs.Volumes {
			out.Volumes = append(out.Volumes, wrapVolume(endpoint, v))
		}
	}
	sort.Slice(out.Volumes, func(i, j int) bool { return out.Volumes[i].Id < out.Volumes[j].Id })
	return out, nil
}
func (r *Router) CreateSnapshot(ctx context.Context, request *pb.SnapshotRequest, opts ...grpc.CallOption) (*pb.Snapshot, error) {
	endpoint, id, err := r.route(request.VolumeId)
	if err != nil {
		return nil, err
	}
	c, err := r.client(endpoint)
	if err != nil {
		return nil, err
	}
	s, err := c.CreateSnapshot(ctx, &pb.SnapshotRequest{Name: request.Name, VolumeId: id}, opts...)
	s = wrapSnapshot(endpoint, s)
	if s != nil {
		s.VolumeId = request.VolumeId
	}
	return s, err
}
func (r *Router) DeleteSnapshot(ctx context.Context, request *pb.ID, opts ...grpc.CallOption) (*pb.Empty, error) {
	endpoint, id, err := r.route(request.Id)
	if status.Code(err) == codes.NotFound {
		return &pb.Empty{}, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := r.client(endpoint)
	if err != nil {
		return nil, err
	}
	return c.DeleteSnapshot(ctx, &pb.ID{Id: id}, opts...)
}
func (r *Router) ListSnapshots(ctx context.Context, request *pb.Empty, opts ...grpc.CallOption) (*pb.Snapshots, error) {
	endpoints, err := r.endpoints(ctx)
	if err != nil {
		return nil, err
	}
	out := &pb.Snapshots{}
	for _, endpoint := range endpoints {
		c, err := r.client(endpoint)
		if err != nil {
			return nil, err
		}
		ss, err := c.ListSnapshots(ctx, request, opts...)
		if err != nil {
			return nil, err
		}
		for _, s := range ss.Snapshots {
			out.Snapshots = append(out.Snapshots, wrapSnapshot(endpoint, s))
		}
	}
	sort.Slice(out.Snapshots, func(i, j int) bool { return out.Snapshots[i].Id < out.Snapshots[j].Id })
	return out, nil
}
func (r *Router) GetCapacity(ctx context.Context, request *pb.CapacityRequest, opts ...grpc.CallOption) (*pb.Capacity, error) {
	selected, _ := ctx.Value(endpointKey).(string)
	var endpoints []string
	var err error
	if selected != "" {
		selected, err = NormalizeEndpoint(selected)
		endpoints = []string{selected}
	} else {
		endpoints, err = r.endpoints(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := &pb.Capacity{}
	for _, endpoint := range endpoints {
		c, err := r.client(endpoint)
		if err != nil {
			return nil, err
		}
		capacity, err := c.GetCapacity(ctx, request, opts...)
		if err != nil {
			return nil, err
		}
		out.AvailableBytes += capacity.AvailableBytes
	}
	return out, nil
}
func (r *Router) AcquireVolume(ctx context.Context, request *pb.VolumeLease, opts ...grpc.CallOption) (*pb.Empty, error) {
	endpoint, id, err := r.route(request.VolumeId)
	if err != nil {
		return nil, err
	}
	c, err := r.client(endpoint)
	if err != nil {
		return nil, err
	}
	return c.AcquireVolume(ctx, &pb.VolumeLease{VolumeId: id, NodeId: request.NodeId}, opts...)
}
func (r *Router) ReleaseVolume(ctx context.Context, request *pb.VolumeLease, opts ...grpc.CallOption) (*pb.Empty, error) {
	endpoint, id, err := r.route(request.VolumeId)
	if err != nil {
		return nil, err
	}
	c, err := r.client(endpoint)
	if err != nil {
		return nil, err
	}
	return c.ReleaseVolume(ctx, &pb.VolumeLease{VolumeId: id, NodeId: request.NodeId}, opts...)
}
func (r *Router) PublishVolume(ctx context.Context, request *pb.VolumePublish, opts ...grpc.CallOption) (*pb.Empty, error) {
	endpoint, id, err := r.route(request.VolumeId)
	if err != nil {
		return nil, err
	}
	c, err := r.client(endpoint)
	if err != nil {
		return nil, err
	}
	return c.PublishVolume(ctx, &pb.VolumePublish{VolumeId: id, NodeId: request.NodeId, Initiator: request.Initiator}, opts...)
}
func (r *Router) UnpublishVolume(ctx context.Context, request *pb.VolumePublish, opts ...grpc.CallOption) (*pb.Empty, error) {
	endpoint, id, err := r.route(request.VolumeId)
	if err != nil {
		return nil, err
	}
	c, err := r.client(endpoint)
	if err != nil {
		return nil, err
	}
	return c.UnpublishVolume(ctx, &pb.VolumePublish{VolumeId: id, NodeId: request.NodeId, Initiator: request.Initiator}, opts...)
}

// Heartbeat reaches every known server, so that each one can tell whether the
// node is alive. It reports the first failure after trying them all.
func (r *Router) Heartbeat(ctx context.Context, request *pb.NodeHeartbeat, opts ...grpc.CallOption) (*pb.Empty, error) {
	endpoints, err := r.endpoints(ctx)
	if err != nil {
		return nil, err
	}
	var first error
	for _, endpoint := range endpoints {
		c, err := r.client(endpoint)
		if err == nil {
			_, err = c.Heartbeat(ctx, request, opts...)
		}
		if err != nil && first == nil {
			first = fmt.Errorf("%s: %w", endpoint, err)
		}
	}
	return &pb.Empty{}, first
}

// FenceNode fences the node on every known server. It fails if any server
// still hears the node or cannot be reached: the node is then not known to be
// cut off everywhere.
func (r *Router) FenceNode(ctx context.Context, request *pb.NodeFence, opts ...grpc.CallOption) (*pb.FencedVolumes, error) {
	endpoints, err := r.endpoints(ctx)
	if err != nil {
		return nil, err
	}
	out := &pb.FencedVolumes{}
	for _, endpoint := range endpoints {
		c, err := r.client(endpoint)
		if err != nil {
			return nil, err
		}
		fenced, err := c.FenceNode(ctx, request, opts...)
		if err != nil {
			return nil, status.Errorf(status.Code(err), "%s: %s", endpoint, status.Convert(err).Message())
		}
		for _, id := range fenced.VolumeIds {
			out.VolumeIds = append(out.VolumeIds, Encode(endpoint, id))
		}
	}
	return out, nil
}
func (r *Router) Metadata(ctx context.Context, request *pb.MetadataRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.Ranges], error) {
	endpoint, id, err := r.route(request.Snapshot)
	if err != nil {
		return nil, err
	}
	base, err := r.source(endpoint, request.BaseSnapshot)
	if err != nil {
		return nil, err
	}
	c, err := r.client(endpoint)
	if err != nil {
		return nil, err
	}
	return c.Metadata(ctx, &pb.MetadataRequest{Snapshot: id, BaseSnapshot: base, StartingOffset: request.StartingOffset, MaxResults: request.MaxResults}, opts...)
}

func (r *Router) String() string {
	return fmt.Sprintf("StorageClass endpoint routing (default %q)", r.fallback)
}
