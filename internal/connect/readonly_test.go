package connect

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Every method that can change cluster state is refused before it leaves the
// process; reads pass through.
func TestReadOnlyTransport(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method)
	}))
	defer srv.Close()

	rt := readOnlyTransport{next: http.DefaultTransport}
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		req, _ := http.NewRequest(m, srv.URL+"/api", nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("%s refused: %v", m, err)
		}
		_ = resp.Body.Close()
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, _ := http.NewRequest(m, srv.URL+"/api/v1/namespaces/x/configmaps/y", nil)
		if _, err := rt.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%s: want a read-only refusal, got %v", m, err)
		}
	}
	if strings.Join(seen, ",") != "GET,HEAD" {
		t.Errorf("server saw %v, want only GET and HEAD", seen)
	}
}

// The clients New builds go through the read-only transport end to end, even
// though it exposes the wrapped transport for request cancellation: a write
// through a real client never reaches the server.
func TestNew_ClientsRefuseWrites(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method)
	}))
	defer srv.Close()

	kc := filepath.Join(t.TempDir(), "kc.yaml")
	content := "apiVersion: v1\nkind: Config\nclusters:\n  - name: s\n    cluster: {server: " + srv.URL + "}\n" +
		"contexts:\n  - name: s\n    context: {cluster: s, user: u}\nusers:\n  - name: u\n    user: {}\n"
	if err := os.WriteFile(kc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	clients, err := New(ClusterRef{Name: "s", Context: "s", Kubeconfig: kc}, 5*time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cms := clients.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace("x")
	if err := cms.Delete(t.Context(), "y", metav1.DeleteOptions{}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("Delete through the dynamic client: want a read-only refusal, got %v", err)
	}
	if len(seen) != 0 {
		t.Errorf("server saw %v, want no request at all", seen)
	}
	if _, ok := interface{}(readOnlyTransport{}).(interface{ WrappedRoundTripper() http.RoundTripper }); !ok {
		t.Error("readOnlyTransport must expose WrappedRoundTripper so client-go can cancel requests")
	}
}
