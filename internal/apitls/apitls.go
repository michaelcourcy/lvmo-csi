// Package apitls secures the management API between the driver and the
// storage servers with mutual TLS. Certificates and CA bundles are read from
// their files at every handshake, so a renewed certificate (for example a
// cert-manager Secret mounted as a volume) takes effect without a restart.
package apitls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
)

// Files names a PEM certificate, its key, and the CA bundle that verifies the
// peer. All three are set, or none.
type Files struct {
	Cert, Key, CA string
}

func (f Files) Enabled() bool { return f.Cert != "" || f.Key != "" || f.CA != "" }

func (f Files) validate() error {
	if f.Cert == "" || f.Key == "" || f.CA == "" {
		return errors.New("TLS needs a certificate, a key and a CA bundle")
	}
	if _, err := f.keyPair(); err != nil {
		return err
	}
	_, err := f.pool()
	return err
}

func (f Files) keyPair() (*tls.Certificate, error) {
	c, err := tls.LoadX509KeyPair(f.Cert, f.Key)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	return &c, nil
}

func (f Files) pool() (*x509.CertPool, error) {
	data, err := os.ReadFile(f.CA)
	if err != nil {
		return nil, fmt.Errorf("read TLS CA bundle: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("no certificate in TLS CA bundle %s", f.CA)
	}
	return pool, nil
}

// Server returns a configuration that only admits clients presenting a
// certificate for client authentication signed by the CA bundle.
func Server(f Files) (*tls.Config, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			c, err := f.keyPair()
			if err != nil {
				return nil, err
			}
			pool, err := f.pool()
			if err != nil {
				return nil, err
			}
			return &tls.Config{
				MinVersion:   tls.VersionTLS12,
				Certificates: []tls.Certificate{*c},
				ClientCAs:    pool,
				ClientAuth:   tls.RequireAndVerifyClientCert,
				NextProtos:   []string{"h2"},
			}, nil
		},
	}, nil
}

// Client returns a configuration for one API endpoint (host:port). The server
// certificate must be signed by the CA bundle, be valid for server
// authentication, and name the endpoint's host, as a DNS name or an IP address.
func Client(f Files, endpoint string) (*tls.Config, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: host,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return f.keyPair()
		},
		// The standard verification would keep the CA bundle loaded at startup;
		// VerifyConnection below does the same checks with the current bundle.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("storage server sent no certificate")
			}
			pool, err := f.pool()
			if err != nil {
				return err
			}
			intermediates := x509.NewCertPool()
			for _, c := range state.PeerCertificates[1:] {
				intermediates.AddCert(c)
			}
			_, err = state.PeerCertificates[0].Verify(x509.VerifyOptions{
				DNSName:       host,
				Roots:         pool,
				Intermediates: intermediates,
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		},
	}, nil
}
