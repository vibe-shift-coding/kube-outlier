package connect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbe_Unreachable(t *testing.T) {
	ref := ClusterRef{
		Name:       "unreachable",
		Context:    "unreachable",
		Kubeconfig: "../../testdata/kubeconfig/unreachable.yaml",
	}

	start := time.Now()
	got := Probe(context.Background(), ref, 500*time.Millisecond)
	elapsed := time.Since(start)

	if got.Reachable {
		t.Fatalf("Probe() Reachable = true, want false")
	}
	if got.SkipReason == "" {
		t.Errorf("Probe() SkipReason is empty, want a reason")
	}
	if elapsed > 3*time.Second {
		t.Errorf("Probe() took %v, want it to respect the ~500ms timeout", elapsed)
	}
}

func TestProbeAll_ConcurrentAndBounded(t *testing.T) {
	refs := []ClusterRef{
		{Name: "a", Context: "unreachable", Kubeconfig: "../../testdata/kubeconfig/unreachable.yaml"},
		{Name: "b", Context: "unreachable", Kubeconfig: "../../testdata/kubeconfig/unreachable.yaml"},
		{Name: "c", Context: "unreachable", Kubeconfig: "../../testdata/kubeconfig/unreachable.yaml"},
	}

	timeout := 500 * time.Millisecond
	start := time.Now()
	got := ProbeAll(context.Background(), refs, timeout)
	elapsed := time.Since(start)

	if len(got) != 3 {
		t.Fatalf("ProbeAll() returned %d refs, want 3", len(got))
	}
	for i, r := range got {
		if r.Name != refs[i].Name {
			t.Errorf("ProbeAll()[%d].Name = %q, want %q (order preserved)", i, r.Name, refs[i].Name)
		}
		if r.Reachable {
			t.Errorf("ProbeAll()[%d].Reachable = true, want false", i)
		}
	}
	if elapsed > 3*timeout {
		t.Errorf("ProbeAll() took %v, want under 3x timeout (%v) given concurrency", elapsed, 3*timeout)
	}
}

// writeServerKubeconfig writes a kubeconfig whose one context, "test", points
// at server with the given user stanza.
func writeServerKubeconfig(t *testing.T, server, user string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	content := "apiVersion: v1\nkind: Config\ncurrent-context: test\n" +
		"clusters:\n  - name: test\n    cluster:\n      server: " + server + "\n" +
		"contexts:\n  - name: test\n    context: {cluster: test, user: test}\n" +
		"users:\n  - name: test\n    user:\n" + user
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A cluster that doesn't answer in time is reported with the timeout and the
// login-plugin hint, not a bare "context deadline exceeded".
func TestProbe_TimeoutReason(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ref := ClusterRef{Name: "slow", Context: "test", Kubeconfig: writeServerKubeconfig(t, srv.URL, "      token: x\n")}
	got := Probe(context.Background(), ref, 300*time.Millisecond)
	if got.Reachable {
		t.Fatal("Reachable = true, want false")
	}
	if !strings.Contains(got.SkipReason, "no answer within --probe-timeout (300ms)") ||
		!strings.Contains(got.SkipReason, "login plugin") {
		t.Errorf("SkipReason = %q, want the timeout and the login-plugin hint", got.SkipReason)
	}
}

// Legacy "auth-provider: oidc" kubeconfigs load as they do in kubectl.
func TestProbe_OIDCAuthProviderSupported(t *testing.T) {
	user := "      auth-provider:\n        name: oidc\n        config:\n" +
		"          client-id: x\n          idp-issuer-url: https://idp.example.invalid\n" +
		"          id-token: not-a-real-token\n"
	ref := ClusterRef{Name: "oidc", Context: "test", Kubeconfig: writeServerKubeconfig(t, "https://127.0.0.1:1", user)}
	if _, err := restConfigFor(ref); err != nil {
		t.Fatalf("restConfigFor: %v", err)
	}
	got := Probe(context.Background(), ref, 500*time.Millisecond)
	if strings.Contains(got.SkipReason, "no Auth Provider found") {
		t.Errorf("SkipReason = %q: the oidc auth provider is not registered", got.SkipReason)
	}
}
