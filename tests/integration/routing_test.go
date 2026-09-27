package integration_test

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/michaelcourcy/lvmo-csi/internal/backend"
	"github.com/michaelcourcy/lvmo-csi/internal/routing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// The harness invokes create, restarts the controller, then invokes check and
// cleanup. Fixture names survive the restart without any driver-local registry.
func TestClusterBackendRouting(t *testing.T) {
	phase := os.Getenv("ROUTING_PHASE")
	if phase == "" {
		t.Skip("requires two live APIs and an in-cluster CSI controller")
	}
	a, b, name := os.Getenv("ROUTING_API_A"), os.Getenv("ROUTING_API_B"), os.Getenv("ROUTING_NAME")
	if a == "" || b == "" || name == "" {
		t.Fatal("routing fixture configuration required")
	}
	conn, err := grpc.NewClient(os.Getenv("CSI_ENDPOINT"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	controller := csi.NewControllerClient(conn)
	type fixture struct{ endpoint, vg, name, volume, snapshot string }
	fixtures := []fixture{}
	for i, endpoint := range []string{a, b} {
		suffix := []string{"-a", "-b"}[i]
		n := name + suffix
		fixtures = append(fixtures, fixture{endpoint: endpoint, vg: []string{"lvmo-test1", "lvmo-route"}[i], name: n, volume: routing.Encode(endpoint, backend.ID("v-", n)), snapshot: routing.Encode(endpoint, backend.ID("s-", n))})
	}
	switch phase {
	case "create":
		capability := &csi.VolumeCapability{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}, AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}}
		for _, f := range fixtures {
			volume, err := controller.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: f.name, CapacityRange: &csi.CapacityRange{RequiredBytes: 128 << 20}, VolumeCapabilities: []*csi.VolumeCapability{capability}, Parameters: map[string]string{"endpoint": f.endpoint, "vg": f.vg, "protocol": "nfs"}})
			if err != nil {
				t.Fatal(err)
			}
			if volume.Volume.VolumeId != f.volume || volume.Volume.VolumeContext["endpoint"] != f.endpoint {
				t.Fatal("volume lost selected backend")
			}
			snapshot, err := controller.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: f.name, SourceVolumeId: f.volume})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Snapshot.SnapshotId != f.snapshot {
				t.Fatal("snapshot lost selected backend")
			}
		}
	case "check":
		volumes, err := controller.ListVolumes(ctx, &csi.ListVolumesRequest{})
		if err != nil {
			t.Fatal(err)
		}
		snapshots, err := controller.ListSnapshots(ctx, &csi.ListSnapshotsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fixtures {
			foundVolume, foundSnapshot := false, false
			for _, v := range volumes.Entries {
				if v.Volume.VolumeId == f.volume {
					foundVolume = true
				}
			}
			for _, s := range snapshots.Entries {
				if s.Snapshot.SnapshotId == f.snapshot && s.Snapshot.SourceVolumeId == f.volume {
					foundSnapshot = true
				}
			}
			if !foundVolume || !foundSnapshot {
				t.Fatalf("cold discovery lost backend %s: volume=%t snapshot=%t", f.endpoint, foundVolume, foundSnapshot)
			}
			capacity, err := controller.GetCapacity(ctx, &csi.GetCapacityRequest{Parameters: map[string]string{"endpoint": f.endpoint, "vg": f.vg}})
			if err != nil || capacity.GetAvailableCapacity() <= 0 {
				t.Fatalf("capacity route failed: %v", err)
			}
		}
		stream, err := csi.NewSnapshotMetadataClient(conn).GetMetadataAllocated(ctx, &csi.GetMetadataAllocatedRequest{SnapshotId: fixtures[1].snapshot, MaxResults: 1})
		if err != nil {
			t.Fatal(err)
		}
		var mapped int64
		for {
			response, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range response.BlockMetadata {
				mapped += r.SizeBytes
			}
		}
		if mapped == 0 {
			t.Fatal("second backend filesystem metadata was not returned")
		}
		_, err = controller.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: name + "-invalid", CapacityRange: &csi.CapacityRange{RequiredBytes: 128 << 20}, VolumeCapabilities: []*csi.VolumeCapability{{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}, AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}}}, Parameters: map[string]string{"endpoint": a, "vg": "lvmo-test1", "protocol": "iscsi"}, VolumeContentSource: &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: fixtures[1].snapshot}}}})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("cross-server clone was not rejected: %v", err)
		}
	case "cleanup":
		for _, f := range fixtures {
			if _, err := controller.DeleteSnapshot(ctx, &csi.DeleteSnapshotRequest{SnapshotId: f.snapshot}); err != nil {
				t.Error(err)
			}
			if _, err := controller.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: f.volume}); err != nil {
				t.Error(err)
			}
		}
	default:
		t.Fatalf("unknown phase %q", phase)
	}
}
