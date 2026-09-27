package main

import (
	"flag"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/michaelcourcy/lvmo-csi/internal/driver"
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
)

var version = "dev"

func main() {
	endpoint := flag.String("endpoint", "unix:///csi/csi.sock", "CSI endpoint")
	api := flag.String("api-endpoint", "", "optional default API endpoint for legacy handles and standalone testing")
	node := flag.String("node-id", "", "node identity")
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
	d := &driver.Driver{API: router, NodeID: *node, Version: version}
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
