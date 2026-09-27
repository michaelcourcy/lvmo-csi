package integration_test

import (
	"bytes"
	"context"
	"fmt"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupReconstruction(t *testing.T) {
	endpoint := os.Getenv("CSI_ENDPOINT")
	if endpoint == "" {
		t.Skip("requires live Linux CSI endpoint")
	}
	conn, e := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	controller := csi.NewControllerClient(conn)
	node := csi.NewNodeClient(conn)
	metadata := csi.NewSnapshotMetadataClient(conn)
	for _, mode := range []string{"nfs", "iscsi-filesystem", "iscsi-block"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			protocol := "iscsi"
			if mode == "nfs" {
				protocol = "nfs"
			}
			cap := &csi.VolumeCapability{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}, AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}}
			if mode == "iscsi-block" {
				cap.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
			}
			name := fmt.Sprintf("backup-%s-%d", mode, time.Now().UnixNano())
			create := func(name string, cap *csi.VolumeCapability, proto string, source string) *csi.Volume {
				t.Helper()
				req := &csi.CreateVolumeRequest{Name: name, CapacityRange: &csi.CapacityRange{RequiredBytes: 128 << 20}, Parameters: map[string]string{"vg": "lvmo-test2", "protocol": proto}, VolumeCapabilities: []*csi.VolumeCapability{cap}}
				if source != "" {
					req.VolumeContentSource = &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: source}}}
				}
				r, e := controller.CreateVolume(ctx, req)
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() {
					if _, e := controller.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: r.Volume.VolumeId}); e != nil {
						t.Error(e)
					}
				})
				return r.Volume
			}
			stage := func(v *csi.Volume, cap *csi.VolumeCapability) string {
				t.Helper()
				path := filepath.Join("/tmp", v.VolumeId)
				_, e := node.NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{VolumeId: v.VolumeId, StagingTargetPath: path, VolumeCapability: cap, VolumeContext: v.VolumeContext})
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() {
					if _, e := node.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{VolumeId: v.VolumeId, StagingTargetPath: path}); e != nil {
						t.Error(e)
					}
				})
				if cap.GetBlock() != nil {
					return path + "/block"
				}
				return path
			}
			v := create(name, cap, protocol, "")
			path := stage(v, cap)
			write := func(value byte) {
				t.Helper()
				file := path + "/payload"
				if mode == "iscsi-block" {
					file = path
				}
				f, e := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0600)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.WriteAt(bytes.Repeat([]byte{value}, 131072), 4<<20); e != nil {
					t.Fatal(e)
				}
				if e = f.Sync(); e != nil {
					t.Fatal(e)
				}
				f.Close()
				if mode != "iscsi-block" {
					if out, e := exec.Command("sync", "-f", path).CombinedOutput(); e != nil {
						t.Fatalf("sync: %v %s", e, out)
					}
				}
			}
			snap := func(suffix string) string {
				t.Helper()
				r, e := controller.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: name + suffix, SourceVolumeId: v.VolumeId})
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() {
					if _, e := controller.DeleteSnapshot(context.Background(), &csi.DeleteSnapshotRequest{SnapshotId: r.Snapshot.SnapshotId}); e != nil {
						t.Error(e)
					}
				})
				return r.Snapshot.SnapshotId
			}
			blockcap := &csi.VolumeCapability{AccessMode: cap.AccessMode, AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}}
			write(0x41)
			s1 := snap("-a")
			clone1 := create(name+"-clone-a", blockcap, "iscsi", s1)
			dev1 := stage(clone1, blockcap)
			image := make([]byte, 128<<20)
			apply := func(dev string, ranges []*csi.BlockMetadata) {
				t.Helper()
				f, e := os.Open(dev)
				if e != nil {
					t.Fatal(e)
				}
				defer f.Close()
				for _, r := range ranges {
					if _, e = f.ReadAt(image[r.ByteOffset:r.ByteOffset+r.SizeBytes], r.ByteOffset); e != nil {
						t.Fatal(e)
					}
				}
			}
			full, e := metadata.GetMetadataAllocated(ctx, &csi.GetMetadataAllocatedRequest{SnapshotId: s1, MaxResults: 1})
			if e != nil {
				t.Fatal(e)
			}
			for {
				r, e := full.Recv()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
				apply(dev1, r.BlockMetadata)
			}
			compare := func(dev string) {
				t.Helper()
				f, e := os.Open(dev)
				if e != nil {
					t.Fatal(e)
				}
				defer f.Close()
				actual := make([]byte, len(image))
				if _, e = io.ReadFull(f, actual); e != nil {
					t.Fatal(e)
				}
				if !bytes.Equal(image, actual) {
					for i := range image {
						if image[i] != actual[i] {
							t.Fatalf("reconstruction mismatch at byte %d", i)
						}
					}
				}
			}
			compare(dev1)
			write(0x42)
			s2 := snap("-b")
			clone2 := create(name+"-clone-b", blockcap, "iscsi", s2)
			dev2 := stage(clone2, blockcap)
			delta, e := metadata.GetMetadataDelta(ctx, &csi.GetMetadataDeltaRequest{BaseSnapshotId: s1, TargetSnapshotId: s2, MaxResults: 1})
			if e != nil {
				t.Fatal(e)
			}
			var changed int64
			for {
				r, e := delta.Recv()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
				apply(dev2, r.BlockMetadata)
				for _, b := range r.BlockMetadata {
					changed += b.SizeBytes
				}
			}
			if changed == 0 || changed >= int64(len(image)) {
				t.Fatalf("expected incremental ranges, got %d bytes", changed)
			}
			compare(dev2)
			t.Logf("%s: verified full + incremental reconstruction; changed %d / %d bytes", mode, changed, len(image))
		})
	}
}
