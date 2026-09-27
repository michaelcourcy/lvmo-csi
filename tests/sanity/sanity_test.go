package sanity_test

import (
	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	"os"
	"testing"
)

func TestCSI(t *testing.T) {
	endpoint := os.Getenv("CSI_ENDPOINT")
	if endpoint == "" {
		t.Skip("set CSI_ENDPOINT to run against a real driver")
	}
	if err := os.MkdirAll("/tmp/lvmo-sanity", 0755); err != nil {
		t.Fatal(err)
	}
	cfg := sanity.NewTestConfig()
	cfg.Address = endpoint
	cfg.TargetPath = "/tmp/lvmo-sanity/target"
	cfg.StagingPath = "/tmp/lvmo-sanity/staging"
	cfg.TestVolumeSize = 128 * 1024 * 1024
	cfg.TestVolumeExpandSize = 256 * 1024 * 1024
	protocol := os.Getenv("PROTOCOL")
	if protocol == "" {
		protocol = "nfs"
	}
	cfg.TestVolumeParameters = map[string]string{"protocol": protocol, "vg": "lvmo-test1"}
	sanity.Test(t, cfg)
}
