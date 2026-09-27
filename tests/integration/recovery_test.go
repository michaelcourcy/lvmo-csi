package integration_test

import (
	"context"
	"fmt"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"os"
	"testing"
	"time"
)

// A real mkfs failure must not strand a PVC name forever. The incomplete LV is
// reclaimed, after which a corrected retry can create a usable volume.
func TestFailedCreateRecovery(t *testing.T) {
	endpoint := os.Getenv("CSI_ENDPOINT")
	if endpoint == "" {
		t.Skip("requires Linux CSI endpoint")
	}
	conn, e := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	controller := csi.NewControllerClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cap := &csi.VolumeCapability{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}, AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "xfs"}}}
	req := &csi.CreateVolumeRequest{Name: fmt.Sprintf("recovery-%d", time.Now().UnixNano()), CapacityRange: &csi.CapacityRange{RequiredBytes: 16 << 20}, Parameters: map[string]string{"vg": "lvmo-test2", "protocol": "iscsi"}, VolumeCapabilities: []*csi.VolumeCapability{cap}}
	if _, e = controller.CreateVolume(ctx, req); e == nil {
		t.Fatal("XFS formatting of undersized LV unexpectedly succeeded")
	}
	req.CapacityRange.RequiredBytes = 512 << 20
	for {
		v, err := controller.CreateVolume(ctx, req)
		if err == nil {
			if _, err = controller.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: v.Volume.VolumeId}); err != nil {
				t.Fatal(err)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("incomplete create was not reclaimed: %v", err)
		case <-time.After(time.Second):
		}
	}
}
