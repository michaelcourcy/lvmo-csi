// lvmo-metadata is an independent backup verification client for the public
// Kubernetes SnapshotMetadata API. It never calls lvmo's management API.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	api "github.com/kubernetes-csi/external-snapshot-metadata/pkg/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"io"
	"log"
	"os"
	"time"
)

func main() {
	endpoint := flag.String("endpoint", "", "discovered metadata service address")
	ca := flag.String("ca", "/tls/ca.crt", "CA certificate path")
	token := flag.String("token-file", "/token/token", "audience-scoped service account token")
	ns := flag.String("namespace", "default", "snapshot namespace")
	snap := flag.String("snapshot", "", "snapshot object name")
	base := flag.String("base", "", "base snapshot CSI handle (incremental)")
	device := flag.String("device", "", "read-only snapshot clone device")
	output := flag.String("output", "/backup/image", "reconstructed image")
	offset := flag.Int64("offset", 0, "resume at byte offset")
	verify := flag.Bool("verify", true, "compare reconstructed image against the entire device")
	maxBatches := flag.Int("max-batches", 0, "stop after this many batches (continuation testing; 0 unlimited)")
	flag.Parse()
	if e := backup(*endpoint, *ca, *token, *ns, *snap, *base, *device, *output, *offset, *verify, *maxBatches); e != nil {
		log.Fatal(e)
	}
}
func backup(endpoint, ca, tokenFile, ns, snap, base, device, output string, offset int64, verify bool, maxBatches int) error {
	pem, e := os.ReadFile(ca)
	if e != nil {
		return e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return fmt.Errorf("invalid CA")
	}
	token, e := os.ReadFile(tokenFile)
	if e != nil {
		return e
	}
	conn, e := grpc.NewClient(endpoint, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})))
	if e != nil {
		return e
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := api.NewSnapshotMetadataClient(conn)
	var receive func() (int64, []*api.BlockMetadata, error)
	if base == "" {
		stream, e := client.GetMetadataAllocated(ctx, &api.GetMetadataAllocatedRequest{SecurityToken: string(token), Namespace: ns, SnapshotName: snap, StartingOffset: offset, MaxResults: 1})
		if e != nil {
			return e
		}
		receive = func() (int64, []*api.BlockMetadata, error) {
			r, e := stream.Recv()
			if e != nil {
				return 0, nil, e
			}
			return r.VolumeCapacityBytes, r.BlockMetadata, nil
		}
	} else {
		stream, e := client.GetMetadataDelta(ctx, &api.GetMetadataDeltaRequest{SecurityToken: string(token), Namespace: ns, TargetSnapshotName: snap, BaseSnapshotId: base, StartingOffset: offset, MaxResults: 1})
		if e != nil {
			return e
		}
		receive = func() (int64, []*api.BlockMetadata, error) {
			r, e := stream.Recv()
			if e != nil {
				return 0, nil, e
			}
			return r.VolumeCapacityBytes, r.BlockMetadata, nil
		}
	}
	// Authenticate and validate the request before creating or truncating output.
	size, ranges, e := receive()
	if e != nil {
		return e
	}
	src, e := os.Open(device)
	if e != nil {
		return e
	}
	defer src.Close()
	flags := os.O_RDWR
	if base == "" && offset == 0 {
		flags |= os.O_CREATE | os.O_TRUNC
	}
	dst, e := os.OpenFile(output, flags, 0600)
	if e != nil {
		return e
	}
	defer dst.Close()
	if e = dst.Truncate(size); e != nil {
		return e
	}
	var changed int64
	end := offset
	batches := 0
	for {
		for _, r := range ranges {
			if r.ByteOffset < end || r.SizeBytes <= 0 || r.ByteOffset > size-r.SizeBytes {
				return fmt.Errorf("invalid or overlapping metadata range")
			}
			if _, e = dst.Seek(r.ByteOffset, io.SeekStart); e != nil {
				return e
			}
			if _, e = io.CopyN(dst, io.NewSectionReader(src, r.ByteOffset, r.SizeBytes), r.SizeBytes); e != nil {
				return e
			}
			changed += r.SizeBytes
			end = r.ByteOffset + r.SizeBytes
		}
		batches++
		if maxBatches > 0 && batches >= maxBatches {
			break
		}
		next, rs, err := receive()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if next != size {
			return fmt.Errorf("capacity changed during stream")
		}
		ranges = rs
	}
	if e = dst.Sync(); e != nil {
		return e
	}
	if verify {
		want, got := sha256.New(), sha256.New()
		if _, e = io.CopyN(want, io.NewSectionReader(src, 0, size), size); e != nil {
			return e
		}
		if _, e = io.CopyN(got, io.NewSectionReader(dst, 0, size), size); e != nil {
			return e
		}
		if fmt.Sprintf("%x", want.Sum(nil)) != fmt.Sprintf("%x", got.Sum(nil)) {
			return fmt.Errorf("reconstructed image differs from snapshot clone")
		}
	}
	fmt.Printf("verified=%t capacity=%d transferred=%d next_offset=%d\n", verify, size, changed, end)
	return nil
}
