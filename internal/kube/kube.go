// Package kube is a minimal Kubernetes API client using the pod's service
// account, for the few reads and deletes the driver needs.
package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

type Client struct {
	http  *http.Client
	base  string
	token func() ([]byte, error)
}

func New(client *http.Client, base string, token func() ([]byte, error)) *Client {
	return &Client{http: client, base: base, token: token}
}

// InCluster returns nil outside a pod. The token is read per request so that
// service-account rotation keeps working.
func InCluster() (*Client, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	if host == "" {
		return nil, nil
	}
	port := os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS")
	if port == "" {
		port = "443"
	}
	const directory = "/var/run/secrets/kubernetes.io/serviceaccount/"
	ca, err := os.ReadFile(directory + "ca.crt")
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("invalid Kubernetes CA")
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	return New(client, "https://"+net.JoinHostPort(host, port), func() ([]byte, error) { return os.ReadFile(directory + "token") }), nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	credentials, err := c.token()
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+string(credentials))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return &StatusError{Code: response.StatusCode, Message: fmt.Sprintf("%s %s: %s", method, path, response.Status)}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}
func (c *Client) Delete(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodDelete, path, body, nil)
}

type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string { return e.Message }
