package main

import (
	"context"
	"flag"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/michaelcourcy/lvmo-csi/internal/driver"
	"github.com/michaelcourcy/lvmo-csi/internal/failover"
	"github.com/michaelcourcy/lvmo-csi/internal/kube"
	"github.com/michaelcourcy/lvmo-csi/internal/routing"
	"github.com/michaelcourcy/lvmo-csi/internal/rpcutil"
	"google.golang.org/grpc"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	endpoint := flag.String("endpoint", "unix:///csi/csi.sock", "CSI endpoint")
	api := flag.String("api-endpoint", "", "optional default API endpoint for legacy handles and standalone testing")
	node := flag.String("node-id", "", "node identity")
	failoverTimeout := flag.Duration("failover-timeout", 0, "controller only: move workloads off a node that Kubernetes and the storage servers have lost for this long (0 disables)")
	flag.Parse()
	discover, e := routing.KubernetesDiscovery(driver.Name)
	if e != nil {
		log.Fatal(e)
	}
	router, e := routing.New(*api, discover)
	if e != nil {
		log.Fatal(e)
	}
	defer router.Close()
	nodeID := ""
	if *node != "" {
		nodeID = driver.NodeID(*node, driver.Initiator())
	}
	d := &driver.Driver{API: router, NodeID: nodeID, Version: version}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if nodeID != "" {
		go d.SendHeartbeats(ctx, 10*time.Second)
	} else if *failoverTimeout > 0 {
		client, err := kube.InCluster()
		if err != nil {
			log.Fatal(err)
		}
		if client != nil {
			c := &failover.Controller{Kube: client, API: router, Driver: driver.Name, Timeout: *failoverTimeout, Now: time.Now}
			go c.Run(ctx, 15*time.Second)
		}
	}
	network, address := "unix", strings.TrimPrefix(*endpoint, "unix://")
	if strings.HasPrefix(*endpoint, "tcp://") {
		network = "tcp"
		address = strings.TrimPrefix(*endpoint, "tcp://")
	} else {
		if e = os.MkdirAll(filepath.Dir(address), 0755); e != nil {
			log.Fatal(e)
		}
		os.Remove(address)
	}
	l, e := net.Listen(network, address)
	if e != nil {
		log.Fatal(e)
	}
	s := grpc.NewServer(grpc.UnaryInterceptor(rpcutil.Unary), grpc.StreamInterceptor(rpcutil.Stream))
	csi.RegisterIdentityServer(s, d)
	csi.RegisterControllerServer(s, d)
	csi.RegisterNodeServer(s, d)
	csi.RegisterSnapshotMetadataServer(s, d)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-signals; rpcutil.Stop(s) }()
	log.Printf("CSI %s listening on %s", driver.Name, *endpoint)
	if e = s.Serve(l); e != nil {
		log.Fatal(e)
	}
}
