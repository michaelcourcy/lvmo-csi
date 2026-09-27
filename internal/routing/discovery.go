package routing

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// KubernetesDiscovery reads only endpoint references, never user kubeconfigs.
// Reading the projected token per request supports service-account rotation.
func KubernetesDiscovery(driverName string) (Discover, error) {
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
	return discoverResources(client, "https://"+net.JoinHostPort(host, port), driverName, func() ([]byte, error) { return os.ReadFile(directory + "token") }), nil
}
func discoverResources(client *http.Client, base, driverName string, token func() ([]byte, error)) Discover {
	return func(ctx context.Context) ([]string, error) {
		endpoints := []string{}
		paths := []string{"/apis/storage.k8s.io/v1/storageclasses", "/api/v1/persistentvolumes", "/apis/snapshot.storage.k8s.io/v1/volumesnapshotcontents"}
		for _, path := range paths {
			credentials, err := token()
			if err != nil {
				return nil, err
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
			if err != nil {
				return nil, err
			}
			request.Header.Set("Authorization", "Bearer "+string(credentials))
			response, err := client.Do(request)
			if err != nil {
				return nil, err
			}
			// Snapshot CRDs are optional when only provisioning is installed.
			if response.StatusCode == http.StatusNotFound && path == paths[2] {
				response.Body.Close()
				continue
			}
			if response.StatusCode != http.StatusOK {
				response.Body.Close()
				return nil, fmt.Errorf("list %s: %s", path, response.Status)
			}
			var list struct {
				Items []struct {
					Provisioner string            `json:"provisioner"`
					Parameters  map[string]string `json:"parameters"`
					Spec        struct {
						Driver string `json:"driver"`
						CSI    *struct {
							Driver       string `json:"driver"`
							VolumeHandle string `json:"volumeHandle"`
						} `json:"csi"`
						Source struct {
							SnapshotHandle string `json:"snapshotHandle"`
						} `json:"source"`
					} `json:"spec"`
					Status struct {
						SnapshotHandle string `json:"snapshotHandle"`
					} `json:"status"`
				} `json:"items"`
			}
			err = json.NewDecoder(response.Body).Decode(&list)
			response.Body.Close()
			if err != nil {
				return nil, err
			}
			for _, item := range list.Items {
				if item.Provisioner == driverName && item.Parameters["endpoint"] != "" {
					endpoints = append(endpoints, item.Parameters["endpoint"])
				}
				handles := []string{}
				if item.Spec.CSI != nil && item.Spec.CSI.Driver == driverName {
					handles = append(handles, item.Spec.CSI.VolumeHandle)
				}
				if item.Spec.Driver == driverName {
					handles = append(handles, item.Spec.Source.SnapshotHandle, item.Status.SnapshotHandle)
				}
				for _, handle := range handles {
					endpoint, _, err := Decode(handle)
					if err != nil {
						return nil, err
					}
					if endpoint != "" {
						endpoints = append(endpoints, endpoint)
					}
				}
			}
		}
		return endpoints, nil
	}
}
