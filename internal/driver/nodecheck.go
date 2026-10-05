package driver

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"time"
)

// NodeCheck holds what CheckNode inspects, so that tests can replace the host.
type NodeCheck struct {
	Node      string
	Initiator func() (string, error)
	// LoadModule loads a kernel module on the host, if it is not loaded yet.
	LoadModule func(ctx context.Context, name string) error
	ISCSID     func() error
	// Peers returns the initiator of every other node running this driver.
	// An error is reported as a warning: it must not block the node plugin.
	Peers func(context.Context) (map[string]string, error)
}

// HostNodeCheck inspects this node. peers may be nil outside a cluster.
func HostNodeCheck(node string, peers func(context.Context) (map[string]string, error)) NodeCheck {
	return NodeCheck{
		Node:      node,
		Initiator: func() (string, error) { return NodeInitiator(node) },
		LoadModule: func(ctx context.Context, name string) error {
			if name != "iscsi_tcp" {
				return fmt.Errorf("unexpected module %s", name)
			}
			return LoadISCSIModule(ctx)
		},
		ISCSID: dialISCSID,
		Peers:  peers,
	}
}

const nodeGuide = "see docs/nodes.md, or set nodeCheck.iscsi=false if this cluster uses only NFS volumes"

// CheckNode prepares this node for iSCSI volumes, loading iscsi_tcp and
// creating lvmo's initiator name, and returns what is still missing.
func CheckNode(ctx context.Context, c NodeCheck) (problems, warnings []string) {
	if err := c.ISCSID(); err != nil {
		problems = append(problems, fmt.Sprintf("iscsid is not reachable (%v): install open-iscsi on the host and enable iscsid", err))
	}
	if err := c.LoadModule(ctx, "iscsi_tcp"); err != nil {
		problems = append(problems, fmt.Sprintf("kernel module iscsi_tcp cannot be loaded (%v): the host's kernel must provide it", err))
	}
	initiator, err := c.Initiator()
	if err != nil {
		problems = append(problems, fmt.Sprintf("cannot create lvmo's initiator name in %s: %v", nodeDir, err))
	} else if c.Peers != nil {
		peers, err := c.Peers(ctx)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("cannot compare initiator names with other nodes: %v", err))
		}
		var same []string
		for node, other := range peers {
			if node != c.Node && other == initiator {
				same = append(same, node)
			}
		}
		if len(same) > 0 {
			problems = append(problems, fmt.Sprintf("initiator name %s is also used by node %s: %s/initiatorname was copied between nodes; delete it on this node, then delete this pod", initiator, strings.Join(same, ", "), nodeDir))
		}
	}
	if len(problems) > 0 {
		problems = append(problems, nodeGuide)
	}
	return problems, warnings
}

// dialISCSID connects to iscsid's control socket, as iscsiadm does. The
// socket is abstract, so it belongs to a network namespace: the host's, since
// the node plugin uses host networking, or the nested test host's.
func dialISCSID() error {
	host := os.Getenv("LVMO_ISCSI_HOST_PROC")
	if host == "" {
		return dialAbstract()
	}
	result := make(chan error, 1)
	go func() {
		// The thread is never unlocked, so Go discards it with its namespace.
		runtime.LockOSThread()
		ns, err := os.Open(host + "/1/ns/net")
		if err != nil {
			result <- err
			return
		}
		defer ns.Close()
		if err = enterNetNamespace(ns); err != nil {
			result <- err
			return
		}
		result <- dialAbstract()
	}()
	return <-result
}
func dialAbstract() error {
	conn, err := net.DialTimeout("unix", "@ISCSIADM_ABSTRACT_NAMESPACE", 5*time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

// Initiators reads the initiator of each node from its CSINode, where the
// node registrar publishes this driver's node ID.
func Initiators(ctx context.Context, get func(context.Context, string, any) error) (map[string]string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Drivers []struct {
					Name   string `json:"name"`
					NodeID string `json:"nodeID"`
				} `json:"drivers"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := get(ctx, "/apis/storage.k8s.io/v1/csinodes", &list); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, item := range list.Items {
		for _, d := range item.Spec.Drivers {
			if d.Name != Name {
				continue
			}
			if _, initiator, ok := parseNodeID(d.NodeID); ok && initiator != "" {
				out[item.Metadata.Name] = initiator
			}
		}
	}
	return out, nil
}
