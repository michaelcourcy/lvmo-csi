// Package failover moves workloads off nodes that are dead to both Kubernetes
// and the storage servers, without waiting for an out-of-service taint.
package failover

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"time"

	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"github.com/michaelcourcy/lvmo-csi/internal/kube"
)

// Controller declares a node dead when Kubernetes has reported it not Ready
// for Timeout and its node plugin has sent no heartbeat to any storage server
// for Timeout. It then fences the node at the storage servers and only after
// that force-deletes the pods on it that use lvmo volumes, so that Kubernetes
// reschedules them and detaches their volumes from the dead node.
type Controller struct {
	Kube    *kube.Client
	API     pb.StorageClient
	Driver  string
	Timeout time.Duration
	Now     func() time.Time
}

func (c *Controller) Run(ctx context.Context, interval time.Duration) {
	log.Printf("failover: enabled, timeout %s", c.Timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := c.Check(ctx); err != nil {
			log.Printf("failover: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type objectMeta struct {
	Name              string  `json:"name"`
	Namespace         string  `json:"namespace"`
	DeletionTimestamp *string `json:"deletionTimestamp"`
}
type nodeList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
		Status   struct {
			Conditions []struct {
				Type               string    `json:"type"`
				Status             string    `json:"status"`
				LastTransitionTime time.Time `json:"lastTransitionTime"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}
type pvList struct {
	Items []struct {
		Spec struct {
			ClaimRef *struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"claimRef"`
			CSI *struct {
				Driver string `json:"driver"`
			} `json:"csi"`
		} `json:"spec"`
	} `json:"items"`
}
type podList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
		Spec     struct {
			Volumes []struct {
				PersistentVolumeClaim *struct {
					ClaimName string `json:"claimName"`
				} `json:"persistentVolumeClaim"`
			} `json:"volumes"`
		} `json:"spec"`
	} `json:"items"`
}

// Check runs one pass over all nodes.
func (c *Controller) Check(ctx context.Context) error {
	var nodes nodeList
	if err := c.Kube.Get(ctx, "/api/v1/nodes", &nodes); err != nil {
		return err
	}
	var errs []error
	for _, node := range nodes.Items {
		for _, condition := range node.Status.Conditions {
			if condition.Type == "Ready" && condition.Status != "True" && c.Now().Sub(condition.LastTransitionTime) >= c.Timeout {
				if err := c.failover(ctx, node.Metadata.Name); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

func (c *Controller) failover(ctx context.Context, node string) error {
	pods, err := c.strandedPods(ctx, node)
	if err != nil || len(pods) == 0 {
		return err
	}
	// Fencing must succeed on every storage server before any pod is deleted:
	// a server that still hears the node refuses, and the node keeps its pods.
	fenced, err := c.API.FenceNode(ctx, &pb.NodeFence{NodeName: node, SilenceSeconds: int64(c.Timeout / time.Second)})
	if err != nil {
		log.Printf("failover: node %s not fenced, pods kept: %v", node, err)
		return nil
	}
	log.Printf("failover: node %s fenced, revoked volumes %v", node, fenced.VolumeIds)
	for _, pod := range pods {
		path := "/api/v1/namespaces/" + url.PathEscape(pod.Namespace) + "/pods/" + url.PathEscape(pod.Name)
		err := c.Kube.Delete(ctx, path, map[string]any{"gracePeriodSeconds": 0})
		var status *kube.StatusError
		if errors.As(err, &status) && status.Code == http.StatusNotFound {
			continue
		}
		if err != nil {
			return err
		}
		log.Printf("failover: deleted pod %s/%s from node %s", pod.Namespace, pod.Name, node)
	}
	return nil
}

// strandedPods lists the pods on a node that use a volume of this driver.
func (c *Controller) strandedPods(ctx context.Context, node string) ([]objectMeta, error) {
	var pods podList
	if err := c.Kube.Get(ctx, "/api/v1/pods?fieldSelector="+url.QueryEscape("spec.nodeName="+node), &pods); err != nil {
		return nil, err
	}
	if len(pods.Items) == 0 {
		return nil, nil
	}
	var pvs pvList
	if err := c.Kube.Get(ctx, "/api/v1/persistentvolumes", &pvs); err != nil {
		return nil, err
	}
	claims := map[string]bool{}
	for _, pv := range pvs.Items {
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == c.Driver && pv.Spec.ClaimRef != nil {
			claims[pv.Spec.ClaimRef.Namespace+"/"+pv.Spec.ClaimRef.Name] = true
		}
	}
	var out []objectMeta
	for _, pod := range pods.Items {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil && claims[pod.Metadata.Namespace+"/"+volume.PersistentVolumeClaim.ClaimName] {
				out = append(out, pod.Metadata)
				break
			}
		}
	}
	return out, nil
}
