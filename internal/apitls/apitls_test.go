package apitls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	hp "google.golang.org/grpc/health/grpc_health_v1"
)

type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

var serial int64

func newCertificate(t *testing.T, template *x509.Certificate, parent *authority) ([]byte, []byte, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial++
	template.SerialNumber = big.NewInt(serial)
	template.NotBefore = time.Now().Add(-time.Hour)
	template.NotAfter = time.Now().Add(time.Hour)
	signer, signerKey := template, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), cert, key
}

func newAuthority(t *testing.T, name string) *authority {
	certPEM, _, cert, key := newCertificate(t, &x509.Certificate{Subject: pkix.Name{CommonName: name}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}, nil)
	return &authority{cert: cert, key: key, pem: certPEM}
}

// leaf writes a certificate signed by ca, with the CA bundle trust, to dir.
func leaf(t *testing.T, dir string, ca *authority, trust []byte, usage x509.ExtKeyUsage, names ...string) Files {
	t.Helper()
	template := &x509.Certificate{Subject: pkix.Name{CommonName: "lvmo"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, name)
		}
	}
	certPEM, keyPEM, _, _ := newCertificate(t, template, ca)
	f := Files{Cert: filepath.Join(dir, "tls.crt"), Key: filepath.Join(dir, "tls.key"), CA: filepath.Join(dir, "ca.crt")}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{f.Cert: certPEM, f.Key: keyPEM, f.CA: trust} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func serve(t *testing.T, f Files) string {
	t.Helper()
	config, err := Server(f)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer(grpc.Creds(credentials.NewTLS(config)))
	hp.RegisterHealthServer(s, health.NewServer())
	go s.Serve(l)
	t.Cleanup(s.Stop)
	return l.Addr().String()
}

func call(t *testing.T, f Files, target, endpoint string) error {
	t.Helper()
	config, err := Client(f, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(config)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = hp.NewHealthClient(conn).Check(ctx, &hp.HealthCheckRequest{})
	return err
}

func TestMutualTLS(t *testing.T) {
	dir := t.TempDir()
	ca, other := newAuthority(t, "lvmo-api-ca"), newAuthority(t, "other-ca")
	server := leaf(t, filepath.Join(dir, "server"), ca, ca.pem, x509.ExtKeyUsageServerAuth, "storage.example", "127.0.0.1")
	address := serve(t, server)
	_, port, _ := net.SplitHostPort(address)
	client := leaf(t, filepath.Join(dir, "client"), ca, ca.pem, x509.ExtKeyUsageClientAuth)

	if err := call(t, client, address, address); err != nil {
		t.Fatalf("driver certificate refused: %v", err)
	}
	// The host name is checked against the endpoint, not the dialled address.
	if err := call(t, client, address, net.JoinHostPort("storage.example", port)); err != nil {
		t.Fatalf("DNS name refused: %v", err)
	}
	if call(t, client, address, net.JoinHostPort("other.example", port)) == nil {
		t.Fatal("server certificate accepted for another host")
	}
	foreign := leaf(t, filepath.Join(dir, "foreign"), other, ca.pem, x509.ExtKeyUsageClientAuth)
	if call(t, foreign, address, address) == nil {
		t.Fatal("client certificate from another CA accepted")
	}
	// A storage server's own certificate must not let it act as a driver.
	if call(t, server, address, address) == nil {
		t.Fatal("server-auth certificate accepted as a client")
	}
	untrusting := leaf(t, filepath.Join(dir, "untrusting"), ca, other.pem, x509.ExtKeyUsageClientAuth)
	if call(t, untrusting, address, address) == nil {
		t.Fatal("server certificate accepted without its CA")
	}
	// A client without any certificate cannot complete the handshake.
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(&tlsNoCert)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = hp.NewHealthClient(conn).Check(ctx, &hp.HealthCheckRequest{}); err == nil {
		t.Fatal("client without certificate accepted")
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	first, second := newAuthority(t, "first"), newAuthority(t, "second")
	both := append(append([]byte{}, first.pem...), second.pem...)
	server := leaf(t, filepath.Join(dir, "server"), first, first.pem, x509.ExtKeyUsageServerAuth, "127.0.0.1")
	address := serve(t, server)
	client := leaf(t, filepath.Join(dir, "client"), first, first.pem, x509.ExtKeyUsageClientAuth)
	if err := call(t, client, address, address); err != nil {
		t.Fatal(err)
	}
	// Replace every file in place while the server keeps running: the next
	// handshakes use the new certificates and bundles.
	leaf(t, filepath.Join(dir, "server"), second, both, x509.ExtKeyUsageServerAuth, "127.0.0.1")
	leaf(t, filepath.Join(dir, "client"), second, both, x509.ExtKeyUsageClientAuth)
	if err := call(t, client, address, address); err != nil {
		t.Fatalf("renewed certificates refused: %v", err)
	}
}

func TestIncompleteFiles(t *testing.T) {
	for _, f := range []Files{{Cert: "a"}, {Cert: "a", Key: "b"}, {Key: "b", CA: "c"}} {
		if !f.Enabled() {
			t.Fatalf("%+v should enable TLS", f)
		}
		if _, err := Server(f); err == nil {
			t.Fatalf("%+v accepted", f)
		}
	}
	if (Files{}).Enabled() {
		t.Fatal("empty files enable TLS")
	}
}

// tlsNoCert trusts any server but offers no client certificate.
var tlsNoCert = tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}

// The charts use two CAs: the server CA, open to every namespace through a
// ClusterIssuer, and the driver CA, usable only in the driver's namespace.
func TestSeparateAuthorities(t *testing.T) {
	dir := t.TempDir()
	serverCA, driverCA := newAuthority(t, "server-ca"), newAuthority(t, "driver-ca")
	address := serve(t, leaf(t, filepath.Join(dir, "server"), serverCA, driverCA.pem, x509.ExtKeyUsageServerAuth, "127.0.0.1"))
	driver := leaf(t, filepath.Join(dir, "driver"), driverCA, serverCA.pem, x509.ExtKeyUsageClientAuth)
	if err := call(t, driver, address, address); err != nil {
		t.Fatalf("driver refused: %v", err)
	}
	// What anyone allowed to use the ClusterIssuer can obtain.
	minted := leaf(t, filepath.Join(dir, "minted"), serverCA, serverCA.pem, x509.ExtKeyUsageClientAuth)
	if call(t, minted, address, address) == nil {
		t.Fatal("client certificate from the server CA accepted")
	}
	// A server signed by the driver CA is not a server the driver trusts.
	impostor := serve(t, leaf(t, filepath.Join(dir, "impostor"), driverCA, driverCA.pem, x509.ExtKeyUsageServerAuth, "127.0.0.1"))
	if call(t, driver, impostor, impostor) == nil {
		t.Fatal("server certificate from the driver CA accepted")
	}
}
