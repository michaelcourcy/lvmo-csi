package driver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func healthyNode() NodeCheck {
	return NodeCheck{
		Node:       "a",
		Initiator:  func() (string, error) { return "iqn.2004-10.com.ubuntu:01:a", nil },
		LoadModule: func(context.Context, string) error { return nil },
		ISCSID:     func() error { return nil },
		Peers: func(context.Context) (map[string]string, error) {
			return map[string]string{"a": "iqn.2004-10.com.ubuntu:01:a", "b": "iqn.2004-10.com.ubuntu:01:b"}, nil
		},
	}
}
func TestHealthyNodePasses(t *testing.T) {
	if problems, warnings := CheckNode(context.Background(), healthyNode()); len(problems)+len(warnings) > 0 {
		t.Fatal(problems, warnings)
	}
}
func TestMissingPrerequisitesAreAllReported(t *testing.T) {
	c := healthyNode()
	c.ISCSID = func() error { return errors.New("connection refused") }
	c.LoadModule = func(context.Context, string) error { return errors.New("module not found") }
	c.Initiator = func() (string, error) { return "", errors.New("read-only file system") }
	problems, _ := CheckNode(context.Background(), c)
	text := strings.Join(problems, "\n")
	for _, want := range []string{"iscsid", "iscsi_tcp", "initiator name", "nodeCheck.iscsi=false"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}
func TestSharedInitiatorFails(t *testing.T) {
	c := healthyNode()
	c.Peers = func(context.Context) (map[string]string, error) {
		return map[string]string{"a": "iqn.2004-10.com.ubuntu:01:a", "b": "iqn.2004-10.com.ubuntu:01:a"}, nil
	}
	problems, _ := CheckNode(context.Background(), c)
	if len(problems) == 0 || !strings.Contains(problems[0], "node b") {
		t.Fatal(problems)
	}
}
func TestUnreadablePeersOnlyWarn(t *testing.T) {
	c := healthyNode()
	c.Peers = func(context.Context) (map[string]string, error) { return nil, errors.New("forbidden") }
	problems, warnings := CheckNode(context.Background(), c)
	if len(problems) > 0 || len(warnings) != 1 {
		t.Fatal(problems, warnings)
	}
}
func TestInitiatorsReadsThisDriverFromCSINodes(t *testing.T) {
	csinodes := `{"items": [
		{"metadata": {"name": "a"}, "spec": {"drivers": [{"name": "lvmo.csi.io", "nodeID": "lvmo:a:iqn:a"}, {"name": "other.csi.io", "nodeID": "a"}]}},
		{"metadata": {"name": "nfs-only"}, "spec": {"drivers": [{"name": "lvmo.csi.io", "nodeID": "lvmo:nfs-only:"}]}}]}`
	get := func(_ context.Context, path string, out any) error {
		if path != "/apis/storage.k8s.io/v1/csinodes" {
			t.Fatal(path)
		}
		return json.Unmarshal([]byte(csinodes), out)
	}
	got, err := Initiators(context.Background(), get)
	if err != nil || len(got) != 1 || got["a"] != "iqn:a" {
		t.Fatal(got, err)
	}
}
func TestNewInitiatorIsAValidIQN(t *testing.T) {
	got := newInitiator("Worker_1.eu-west-3.compute.internal", "0123456789abcdef")
	if got != "iqn.2026-09.io.lvmo.node:worker1.eu-west-3.compute.internal:0123456789abcdef" {
		t.Fatal(got)
	}
	long := newInitiator(strings.Repeat("a", 300), "0123456789abcdef")
	if len(long) > 223 {
		t.Fatal(len(long))
	}
}
func TestIfaceNameFitsOpenISCSI(t *testing.T) {
	a, b := ifaceName("iqn:a"), ifaceName("iqn:b")
	if a == b || len(a) >= 65 || !strings.HasPrefix(a, "lvmo-") {
		t.Fatal(a, b)
	}
}
