package driver

import (
	"context"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNodeIDCarriesInitiator(t *testing.T) {
	id := NodeID("worker-1", "iqn.2004-10.com.ubuntu:01:465356933dee")
	node, initiator, ok := parseNodeID(id)
	if !ok || node != "worker-1" || initiator != "iqn.2004-10.com.ubuntu:01:465356933dee" {
		t.Fatalf("round trip failed: %q %q %v", node, initiator, ok)
	}
	if node, initiator, ok = parseNodeID(NodeID("nfs-only", "")); !ok || node != "nfs-only" || initiator != "" {
		t.Fatalf("node without initiator rejected: %q %q %v", node, initiator, ok)
	}
	for _, foreign := range []string{"", "worker-1", "fake-node-id-1234", "lvmo:", "lvmo::iqn.x"} {
		if _, _, ok = parseNodeID(foreign); ok {
			t.Fatalf("accepted node ID not issued by lvmo: %q", foreign)
		}
	}
}

// Kubernetes and csi-sanity expect NotFound for a node the driver never issued.
func TestPublishToUnknownNodeIsNotFound(t *testing.T) {
	d := &Driver{}
	capability := &csi.VolumeCapability{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}, AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}}
	_, e := d.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{VolumeId: "v", NodeId: "fake-node-id-1234", VolumeCapability: capability})
	if status.Code(e) != codes.NotFound {
		t.Fatalf("unknown node: %v", e)
	}
	if _, e = d.ControllerUnpublishVolume(context.Background(), &csi.ControllerUnpublishVolumeRequest{VolumeId: "v", NodeId: "fake-node-id-1234"}); e != nil {
		t.Fatalf("unpublishing an unknown node must succeed: %v", e)
	}
}
