package main

import (
	"context"
	"flag"
	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"github.com/michaelcourcy/lvmo-csi/internal/apitls"
	"github.com/michaelcourcy/lvmo-csi/internal/backend"
	"github.com/michaelcourcy/lvmo-csi/internal/rpcutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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
	iscsiListen := flag.String("iscsi-listen-address", "", "local iSCSI portal address; requires targetcli auto_add_default_portal=false")
	pool := flag.String("pool", "lvmo-pool", "existing thin pool name in every VG")
	clients := flag.String("nfs-clients", "*", "NFS export client selector")
	nfsInsecure := flag.Bool("nfs-insecure", false, "allow NFS clients to connect from unprivileged source ports")
	queueDepth := flag.Int("iscsi-queue-depth", 8, "commands in flight per iSCSI volume, on the target and the nodes; bounds latency when a disk is overloaded (0 keeps the defaults)")
	commandTimeout := flag.Duration("iscsi-command-timeout", 120*time.Second, "SCSI command timeout nodes set on lvmo iSCSI disks (0 keeps the node default)")
	var api apitls.Files
	flag.StringVar(&api.Cert, "tls-cert", "", "PEM server certificate for the API; with --tls-key and --tls-client-ca, only clients with a certificate signed by that CA are served")
	flag.StringVar(&api.Key, "tls-key", "", "PEM key of --tls-cert")
	flag.StringVar(&api.CA, "tls-client-ca", "", "PEM CA bundle that signs driver client certificates")
	flag.Parse()
	options := []grpc.ServerOption{grpc.UnaryInterceptor(rpcutil.Unary), grpc.StreamInterceptor(rpcutil.Stream)}
	if api.Enabled() {
		config, err := apitls.Server(api)
		if err != nil {
			log.Fatal(err)
		}
		options = append(options, grpc.Creds(credentials.NewTLS(config)))
	} else {
		log.Print("WARNING: the API has no TLS: anyone who can reach its port can read and delete volumes; set --tls-cert, --tls-key and --tls-client-ca")
	}
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
	b, e := backend.New(backend.Config{Root: *root, Server: *server, ISCSIListenAddress: *iscsiListen, Pool: *pool, Clients: *clients, NFSInsecure: *nfsInsecure, VGs: flag.Args(), ISCSIQueueDepth: *queueDepth, ISCSICommandTimeout: *commandTimeout}, backend.Exec{})
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
	s := grpc.NewServer(options...)
	pb.RegisterStorageServer(s, b)
	h := health.NewServer()
	h.SetServingStatus("", hp.HealthCheckResponse_SERVING)
	hp.RegisterHealthServer(s, h)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-signals; rpcutil.Stop(s) }()
	log.Printf("lvmo API listening on %s (mutual TLS: %t)", l.Addr(), api.Enabled())
	if e = s.Serve(l); e != nil {
		log.Fatal(e)
	}
}
