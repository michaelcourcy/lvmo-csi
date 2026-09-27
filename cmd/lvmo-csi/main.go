package main

import (
	"context"
	"flag"
	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"github.com/michaelcourcy/lvmo-csi/internal/backend"
	"github.com/michaelcourcy/lvmo-csi/internal/rpcutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	hp "google.golang.org/grpc/health/grpc_health_v1"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	port := flag.String("P", "50051", "gRPC listen port")
	root := flag.String("root", "/var/lib/lvmo", "state and mount directory")
	server := flag.String("server", "", "NFS/iSCSI address reachable from Kubernetes nodes")
	pool := flag.String("pool", "lvmo-pool", "existing thin pool name in every VG")
	clients := flag.String("nfs-clients", "*", "NFS export client selector")
	flag.Parse()
	if *server == "" {
		// UDP connect selects a local route without sending a packet.
		conn, err := net.Dial("udp4", "192.0.2.1:9")
		if err != nil {
			log.Fatal("cannot select an advertised address; specify --server: ", err)
		}
		*server = conn.LocalAddr().(*net.UDPAddr).IP.String()
		conn.Close()
	}
	log.Printf("advertising storage address %s", *server)
	b, e := backend.New(backend.Config{Root: *root, Server: *server, Pool: *pool, Clients: *clients, VGs: flag.Args()}, backend.Exec{})
	if e != nil {
		log.Fatal(e)
	}
	lock, e := os.OpenFile(*root+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		log.Fatal(e)
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		log.Fatal("another lvmo process owns this state directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	e = b.Reconcile(ctx)
	cancel()
	if e != nil {
		log.Fatal(e)
	}
	go b.Reap(context.Background())
	l, e := net.Listen("tcp", ":"+*port)
	if e != nil {
		log.Fatal(e)
	}
	s := grpc.NewServer(grpc.UnaryInterceptor(rpcutil.Unary), grpc.StreamInterceptor(rpcutil.Stream))
	pb.RegisterStorageServer(s, b)
	h := health.NewServer()
	h.SetServingStatus("", hp.HealthCheckResponse_SERVING)
	hp.RegisterHealthServer(s, h)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-signals; rpcutil.Stop(s) }()
	log.Printf("lvmo API listening on %s", l.Addr())
	if e = s.Serve(l); e != nil {
		log.Fatal(e)
	}
}
