package failover

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"github.com/michaelcourcy/lvmo-csi/internal/kube"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeAPI struct {
	pb.StorageClient
	fenceErr error
	fenced   []string
}

func (f *fakeAPI) FenceNode(_ context.Context, r *pb.NodeFence, _ ...grpc.CallOption) (*pb.FencedVolumes, error) {
	f.fenced = append(f.fenced, r.NodeName)
	if f.fenceErr != nil {
		return nil, f.fenceErr
	}
	return &pb.FencedVolumes{VolumeIds: []string{"v-1"}}, nil
}

// cluster serves a dead node "a" with a pod on an lvmo volume and a pod on
// another driver's volume, and a healthy node "b" with an lvmo pod.
func cluster(t *testing.T, notReadySince time.Time, deleted *[]string) *kube.Client {
	mux := http.NewServeMux()
	reply := func(path string, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				*deleted = append(*deleted, r.URL.Path)
				return
			}
			_, _ = w.Write([]byte(body))
		})
	}
	since, _ := json.Marshal(notReadySince)
	reply("/api/v1/nodes", `{"items":[
		{"metadata":{"name":"a"},"status":{"conditions":[{"type":"Ready","status":"Unknown","lastTransitionTime":`+string(since)+`}]}},
		{"metadata":{"name":"b"},"status":{"conditions":[{"type":"Ready","status":"True","lastTransitionTime":"2026-01-01T00:00:00Z"}]}}]}`)
	reply("/api/v1/persistentvolumes", `{"items":[
		{"spec":{"claimRef":{"namespace":"app","name":"lvmo-data"},"csi":{"driver":"lvmo.csi.io"}}},
		{"spec":{"claimRef":{"namespace":"app","name":"ebs-data"},"csi":{"driver":"ebs.csi.aws.com"}}}]}`)
	mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fieldSelector") != "spec.nodeName=a" {
			t.Errorf("pods listed for an unexpected node: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"items":[
			{"metadata":{"namespace":"app","name":"db"},"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"lvmo-data"}}]}},
			{"metadata":{"namespace":"app","name":"cache"},"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"ebs-data"}}]}},
			{"metadata":{"namespace":"app","name":"web"},"spec":{"volumes":[{"emptyDir":{}}]}}]}`))
	})
	mux.HandleFunc("/api/v1/namespaces/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			*deleted = append(*deleted, r.URL.Path)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return kube.New(server.Client(), server.URL, func() ([]byte, error) { return []byte("token"), nil })
}

func TestDeadNodeIsFencedThenOnlyLvmoPodsDeleted(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	var deleted []string
	api := &fakeAPI{}
	c := &Controller{Kube: cluster(t, now.Add(-3*time.Minute), &deleted), API: api, Driver: "lvmo.csi.io", Timeout: 2 * time.Minute, Now: func() time.Time { return now }}
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.fenced) != 1 || api.fenced[0] != "a" {
		t.Fatalf("expected node a fenced once: %v", api.fenced)
	}
	if strings.Join(deleted, ",") != "/api/v1/namespaces/app/pods/db" {
		t.Fatalf("only the lvmo pod on node a should be deleted: %v", deleted)
	}
}

func TestRecentlyLostNodeIsLeftAlone(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	var deleted []string
	api := &fakeAPI{}
	c := &Controller{Kube: cluster(t, now.Add(-time.Minute), &deleted), API: api, Driver: "lvmo.csi.io", Timeout: 2 * time.Minute, Now: func() time.Time { return now }}
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.fenced) != 0 || len(deleted) != 0 {
		t.Fatalf("acted before the timeout: fenced %v deleted %v", api.fenced, deleted)
	}
}

// A storage server that still hears the node, or cannot be reached, prevents
// any pod deletion: the node may still be writing.
func TestNoPodDeletedWithoutFencing(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, err := range []error{status.Error(codes.FailedPrecondition, "node was heard 5s ago"), status.Error(codes.Unavailable, "server down")} {
		var deleted []string
		api := &fakeAPI{fenceErr: err}
		c := &Controller{Kube: cluster(t, now.Add(-3*time.Minute), &deleted), API: api, Driver: "lvmo.csi.io", Timeout: 2 * time.Minute, Now: func() time.Time { return now }}
		if e := c.Check(context.Background()); e != nil {
			t.Fatal(e)
		}
		if len(deleted) != 0 {
			t.Fatalf("pods deleted although fencing failed with %v: %v", err, deleted)
		}
	}
}
