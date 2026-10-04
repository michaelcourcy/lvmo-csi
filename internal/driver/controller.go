package driver

import (
	"context"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"github.com/michaelcourcy/lvmo-csi/internal/routing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"io"
	"strconv"
	"strings"
	"time"
)

const Name = "lvmo.csi.io"

// NodeID carries the node's iSCSI initiator name, possibly empty, so that the
// controller can grant that node access to a target: lvmo:<node>:<initiator>.
func NodeID(node, initiator string) string { return "lvmo:" + node + ":" + initiator }
func parseNodeID(id string) (node, initiator string, ok bool) {
	rest, ok := strings.CutPrefix(id, "lvmo:")
	if !ok {
		return "", "", false
	}
	node, initiator, ok = strings.Cut(rest, ":")
	return node, initiator, ok && node != ""
}

type Driver struct {
	csi.UnimplementedIdentityServer
	csi.UnimplementedControllerServer
	csi.UnimplementedNodeServer
	csi.UnimplementedSnapshotMetadataServer
	API             pb.StorageClient
	NodeID, Version string
}

func (d *Driver) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{Name: Name, VendorVersion: d.Version}, nil
}
func (d *Driver) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	out := &csi.GetPluginCapabilitiesResponse{}
	for _, t := range []csi.PluginCapability_Service_Type{csi.PluginCapability_Service_CONTROLLER_SERVICE, csi.PluginCapability_Service_SNAPSHOT_METADATA_SERVICE} {
		out.Capabilities = append(out.Capabilities, &csi.PluginCapability{Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{Type: t}}})
	}
	out.Capabilities = append(out.Capabilities, &csi.PluginCapability{Type: &csi.PluginCapability_VolumeExpansion_{VolumeExpansion: &csi.PluginCapability_VolumeExpansion{Type: csi.PluginCapability_VolumeExpansion_ONLINE}}})
	return out, nil
}
func (d *Driver) Probe(ctx context.Context, _ *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	// Readiness describes the router, not the availability of every configured
	// VM. An unavailable backend must not prevent provisioning on another VM.
	return &csi.ProbeResponse{Ready: wrapperspb.Bool(true)}, nil
}
func validate(caps []*csi.VolumeCapability, protocol string) error {
	if len(caps) == 0 {
		return status.Error(codes.InvalidArgument, "volume capabilities required")
	}
	for _, c := range caps {
		if c == nil || c.AccessMode == nil || c.AccessMode.Mode == csi.VolumeCapability_AccessMode_UNKNOWN || c.GetAccessType() == nil {
			return status.Error(codes.InvalidArgument, "invalid capability")
		}
		if protocol == "nfs" && c.GetBlock() != nil {
			return status.Error(codes.InvalidArgument, "NFS requires filesystem access")
		}
		if protocol == "iscsi" && (c.AccessMode.Mode == csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER || c.AccessMode.Mode == csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER || c.AccessMode.Mode == csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY) {
			return status.Error(codes.InvalidArgument, "multi-node iSCSI access unsupported")
		}
		if m := c.GetMount(); m != nil && protocol == "iscsi" && m.FsType != "" && m.FsType != "ext4" && m.FsType != "xfs" {
			return status.Error(codes.InvalidArgument, "unsupported filesystem")
		}
	}
	return nil
}
func capacity(r *csi.CapacityRange) (int64, error) {
	n := int64(1024 * 1024 * 1024)
	if r != nil {
		if r.RequiredBytes < 0 || r.LimitBytes < 0 {
			return 0, status.Error(codes.InvalidArgument, "negative capacity")
		}
		if r.RequiredBytes > 0 {
			n = r.RequiredBytes
		}
		n = (n + 4194303) / 4194304 * 4194304
		if n <= 0 || n > 1<<60 || r.LimitBytes > 0 && n > r.LimitBytes {
			return 0, status.Error(codes.OutOfRange, "capacity outside limits")
		}
	}
	return n, nil
}
func volume(v *pb.Volume) *csi.Volume {
	attributes := map[string]string{"protocol": v.Protocol, "server": v.Server, "path": v.Path, "iqn": v.Iqn, "filesystem": v.Filesystem}
	if endpoint, _, err := routing.Decode(v.Id); err == nil && endpoint != "" {
		attributes["endpoint"] = endpoint
	}
	return &csi.Volume{VolumeId: v.Id, CapacityBytes: v.Bytes, VolumeContext: attributes}
}

func snapshot(s *pb.Snapshot) *csi.Snapshot {
	return &csi.Snapshot{SnapshotId: s.Id, SourceVolumeId: s.VolumeId, SizeBytes: s.Bytes, CreationTime: timestamppb.New(time.Unix(s.CreatedUnix, 0)), ReadyToUse: s.Ready}
}
func (d *Driver) CreateVolume(ctx context.Context, r *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if r.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
	}
	p := r.Parameters["protocol"]
	if p == "" {
		p = "nfs"
	}
	if p != "nfs" && p != "iscsi" {
		return nil, status.Error(codes.InvalidArgument, "unsupported protocol")
	}
	if e := validate(r.VolumeCapabilities, p); e != nil {
		return nil, e
	}
	ctx = routing.WithEndpoint(ctx, r.Parameters["endpoint"])
	size, e := capacity(r.CapacityRange)
	if e != nil {
		return nil, e
	}
	fs := r.Parameters["filesystem"]
	block := r.VolumeCapabilities[0].GetBlock() != nil
	for _, c := range r.VolumeCapabilities {
		if (c.GetBlock() != nil) != block {
			return nil, status.Error(codes.InvalidArgument, "mixed access types")
		}
	}
	if m := r.VolumeCapabilities[0].GetMount(); m != nil && m.FsType != "" && m.FsType != "nfs" && m.FsType != "nfs4" {
		fs = m.FsType
	}
	req := &pb.CreateVolumeRequest{Name: r.Name, Bytes: size, Vg: r.Parameters["vg"], Protocol: p, Filesystem: fs, Block: block}
	if r.VolumeContentSource != nil {
		switch s := r.VolumeContentSource.Type.(type) {
		case *csi.VolumeContentSource_Snapshot:
			req.SourceSnapshot = s.Snapshot.SnapshotId
			if req.SourceSnapshot == "" {
				return nil, status.Error(codes.InvalidArgument, "snapshot id required")
			}
		case *csi.VolumeContentSource_Volume:
			req.SourceVolume = s.Volume.VolumeId
			if req.SourceVolume == "" {
				return nil, status.Error(codes.InvalidArgument, "source id required")
			}
		default:
			return nil, status.Error(codes.InvalidArgument, "invalid source")
		}
	}
	v, e := d.API.CreateVolume(ctx, req)
	if e != nil {
		return nil, e
	}
	out := volume(v)
	out.ContentSource = r.VolumeContentSource
	return &csi.CreateVolumeResponse{Volume: out}, nil
}
func (d *Driver) DeleteVolume(ctx context.Context, r *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if r.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	_, e := d.API.DeleteVolume(ctx, &pb.ID{Id: r.VolumeId})
	return &csi.DeleteVolumeResponse{}, e
}
func (d *Driver) ValidateVolumeCapabilities(ctx context.Context, r *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if len(r.VolumeCapabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument, "capabilities required")
	}
	if r.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	v, e := d.API.GetVolume(ctx, &pb.ID{Id: r.VolumeId})
	if e != nil {
		return nil, e
	}
	if e = validate(r.VolumeCapabilities, v.Protocol); e != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{Message: e.Error()}, nil
	}
	return &csi.ValidateVolumeCapabilitiesResponse{Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{VolumeCapabilities: r.VolumeCapabilities, VolumeContext: r.VolumeContext, Parameters: r.Parameters}}, nil
}
func (d *Driver) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	out := &csi.ControllerGetCapabilitiesResponse{}
	for _, t := range []csi.ControllerServiceCapability_RPC_Type{csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME, csi.ControllerServiceCapability_RPC_LIST_VOLUMES, csi.ControllerServiceCapability_RPC_GET_CAPACITY, csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT, csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS, csi.ControllerServiceCapability_RPC_CLONE_VOLUME, csi.ControllerServiceCapability_RPC_EXPAND_VOLUME, csi.ControllerServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER, csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME} {
		out.Capabilities = append(out.Capabilities, &csi.ControllerServiceCapability{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: t}}})
	}
	return out, nil
}

// ControllerPublishVolume grants the node's initiator access to an iSCSI target.
// NFS volumes need no attach step and succeed unchanged.
func (d *Driver) ControllerPublishVolume(ctx context.Context, r *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	if r.VolumeId == "" || r.NodeId == "" || r.VolumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "volume, node, and capability required")
	}
	_, initiator, ok := parseNodeID(r.NodeId)
	if !ok {
		return nil, status.Error(codes.NotFound, "node not found")
	}
	v, e := d.API.GetVolume(ctx, &pb.ID{Id: r.VolumeId})
	if e != nil {
		return nil, e
	}
	if e = validate([]*csi.VolumeCapability{r.VolumeCapability}, v.Protocol); e != nil {
		return nil, e
	}
	if _, e = d.API.PublishVolume(ctx, &pb.VolumePublish{VolumeId: r.VolumeId, NodeId: r.NodeId, Initiator: initiator}); e != nil {
		return nil, e
	}
	return &csi.ControllerPublishVolumeResponse{}, nil
}

// ControllerUnpublishVolume revokes the node at the target, which also closes its
// sessions. Kubernetes calls it after a confirmed node failure to fence the node.
func (d *Driver) ControllerUnpublishVolume(ctx context.Context, r *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	if r.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume required")
	}
	if _, _, ok := parseNodeID(r.NodeId); r.NodeId != "" && !ok {
		return &csi.ControllerUnpublishVolumeResponse{}, nil
	}
	_, e := d.API.UnpublishVolume(ctx, &pb.VolumePublish{VolumeId: r.VolumeId, NodeId: r.NodeId})
	if status.Code(e) == codes.NotFound {
		e = nil
	}
	if e != nil {
		return nil, e
	}
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}
func page(token string, maxEntries int32, total int) (int, int, string, error) {
	start := 0
	var e error
	if token != "" {
		start, e = strconv.Atoi(token)
	}
	if e != nil || start < 0 || start > total {
		return 0, 0, "", status.Error(codes.Aborted, "invalid starting token")
	}
	if maxEntries < 0 {
		return 0, 0, "", status.Error(codes.InvalidArgument, "negative max entries")
	}
	end := total
	next := ""
	if maxEntries > 0 && start+int(maxEntries) < end {
		end = start + int(maxEntries)
		next = strconv.Itoa(end)
	}
	return start, end, next, nil
}
func (d *Driver) ListVolumes(ctx context.Context, r *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	vs, e := d.API.ListVolumes(ctx, &pb.Empty{})
	if e != nil {
		return nil, e
	}
	start, end, next, e := page(r.StartingToken, r.MaxEntries, len(vs.Volumes))
	if e != nil {
		return nil, e
	}
	out := &csi.ListVolumesResponse{NextToken: next}
	for _, v := range vs.Volumes[start:end] {
		out.Entries = append(out.Entries, &csi.ListVolumesResponse_Entry{Volume: volume(v)})
	}
	return out, nil
}
func (d *Driver) GetCapacity(ctx context.Context, r *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	if len(r.VolumeCapabilities) > 0 {
		p := r.Parameters["protocol"]
		if p == "" {
			p = "nfs"
		}
		if validate(r.VolumeCapabilities, p) != nil {
			return &csi.GetCapacityResponse{}, nil
		}
	}
	v, e := d.API.GetCapacity(routing.WithEndpoint(ctx, r.Parameters["endpoint"]), &pb.CapacityRequest{Vg: r.Parameters["vg"]})
	if e != nil {
		return nil, e
	}
	return &csi.GetCapacityResponse{AvailableCapacity: v.AvailableBytes}, nil
}
func (d *Driver) CreateSnapshot(ctx context.Context, r *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	s, e := d.API.CreateSnapshot(ctx, &pb.SnapshotRequest{Name: r.Name, VolumeId: r.SourceVolumeId})
	if e != nil {
		return nil, e
	}
	return &csi.CreateSnapshotResponse{Snapshot: snapshot(s)}, nil
}
func (d *Driver) DeleteSnapshot(ctx context.Context, r *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	_, e := d.API.DeleteSnapshot(ctx, &pb.ID{Id: r.SnapshotId})
	return &csi.DeleteSnapshotResponse{}, e
}
func (d *Driver) ListSnapshots(ctx context.Context, r *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	if r.SnapshotId != "" {
		ctx = routing.WithHandle(ctx, r.SnapshotId)
	} else if r.SourceVolumeId != "" {
		ctx = routing.WithHandle(ctx, r.SourceVolumeId)
	}
	ss, e := d.API.ListSnapshots(ctx, &pb.Empty{})
	if status.Code(e) == codes.NotFound {
		return &csi.ListSnapshotsResponse{}, nil
	}
	if e != nil {
		return nil, e
	}
	filtered := []*pb.Snapshot{}
	for _, s := range ss.Snapshots {
		if (r.SnapshotId == "" || s.Id == r.SnapshotId) && (r.SourceVolumeId == "" || s.VolumeId == r.SourceVolumeId) {
			filtered = append(filtered, s)
		}
	}
	start, end, next, e := page(r.StartingToken, r.MaxEntries, len(filtered))
	if e != nil {
		return nil, e
	}
	out := &csi.ListSnapshotsResponse{NextToken: next}
	for _, s := range filtered[start:end] {
		out.Entries = append(out.Entries, &csi.ListSnapshotsResponse_Entry{Snapshot: snapshot(s)})
	}
	return out, nil
}
func (d *Driver) ControllerExpandVolume(ctx context.Context, r *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	if r.VolumeId == "" || r.CapacityRange == nil {
		return nil, status.Error(codes.InvalidArgument, "id and capacity required")
	}
	size, e := capacity(r.CapacityRange)
	if e != nil {
		return nil, e
	}
	v, e := d.API.ExpandVolume(ctx, &pb.ExpandRequest{Id: r.VolumeId, Bytes: size})
	if e != nil {
		return nil, e
	}
	return &csi.ControllerExpandVolumeResponse{CapacityBytes: v.Bytes, NodeExpansionRequired: v.Protocol == "iscsi"}, nil
}
func (d *Driver) metadata(ctx context.Context, r *pb.MetadataRequest, send func(int64, []*csi.BlockMetadata) error) error {
	stream, e := d.API.Metadata(ctx, r)
	if e != nil {
		return e
	}
	for {
		msg, e := stream.Recv()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		ranges := []*csi.BlockMetadata{}
		for _, b := range msg.Ranges {
			ranges = append(ranges, &csi.BlockMetadata{ByteOffset: b.Offset, SizeBytes: b.Length})
		}
		if e = send(msg.Capacity, ranges); e != nil {
			return e
		}
	}
}
func (d *Driver) GetMetadataAllocated(r *csi.GetMetadataAllocatedRequest, s csi.SnapshotMetadata_GetMetadataAllocatedServer) error {
	return d.metadata(s.Context(), &pb.MetadataRequest{Snapshot: r.SnapshotId, StartingOffset: r.StartingOffset, MaxResults: r.MaxResults}, func(size int64, ranges []*csi.BlockMetadata) error {
		return s.Send(&csi.GetMetadataAllocatedResponse{BlockMetadataType: csi.BlockMetadataType_VARIABLE_LENGTH, VolumeCapacityBytes: size, BlockMetadata: ranges})
	})
}
func (d *Driver) GetMetadataDelta(r *csi.GetMetadataDeltaRequest, s csi.SnapshotMetadata_GetMetadataDeltaServer) error {
	if r.BaseSnapshotId == "" {
		return status.Error(codes.InvalidArgument, "base snapshot required")
	}
	return d.metadata(s.Context(), &pb.MetadataRequest{Snapshot: r.TargetSnapshotId, BaseSnapshot: r.BaseSnapshotId, StartingOffset: r.StartingOffset, MaxResults: r.MaxResults}, func(size int64, ranges []*csi.BlockMetadata) error {
		return s.Send(&csi.GetMetadataDeltaResponse{BlockMetadataType: csi.BlockMetadataType_VARIABLE_LENGTH, VolumeCapacityBytes: size, BlockMetadata: ranges})
	})
}
