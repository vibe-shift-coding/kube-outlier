package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/connect"
	"github.com/vibe-shift-coding/kube-outlier/internal/discovery"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

var secretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

var secretKey = identity.ResourceKey{Kind: "Secret", Namespace: "payments", Name: "db"}

// secretCluster is one scanned cluster holding the db Secret with the given
// password, served through a fake dynamic client.
func secretCluster(t *testing.T, cluster, password string) clusterResult {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{
			"name":      "db",
			"namespace": "payments",
			"annotations": map[string]interface{}{
				"kubectl.kubernetes.io/last-applied-configuration": `{"stringData":{"password":"` + password + `"}}`,
			},
		},
		"type":       "Opaque",
		"stringData": map[string]interface{}{"password": password},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{secretGVR: "SecretList"}, obj)
	return clusterResult{
		snap: &inventory.Snapshot{Cluster: cluster, Resources: []inventory.Resource{{
			Key:               secretKey,
			Cluster:           cluster,
			ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply"}},
			CreationTimestamp: metav1.NewTime(testNow),
		}}},
		gvrs:    []discovery.GVRInfo{{GVR: secretGVR, Kind: "Secret", Namespaced: true}},
		clients: &connect.Clients{Dynamic: dyn},
	}
}

// assertNoSecret fails if out contains a password or its plain SHA-256, the
// form a dictionary attack would check guesses against.
func assertNoSecret(t *testing.T, what, out string, passwords ...string) {
	t.Helper()
	for _, p := range passwords {
		sum := sha256.Sum256([]byte(p))
		if strings.Contains(out, p) || strings.Contains(out, hex.EncodeToString(sum[:])) {
			t.Fatalf("%s contains a Secret value or its plain sha256:\n%s", what, out)
		}
	}
}

// Comparing Secrets across clusters reports that they differ without
// anything that can be checked against a guessed value.
func TestContentComparison_SecretValuesNeverReachTheReport(t *testing.T) {
	cfg := config.Default()
	cfg.Content.Kinds = []string{"Secret"}
	results := []clusterResult{
		secretCluster(t, "a", "hunter2-a"),
		secretCluster(t, "b", "hunter2-a"),
		secretCluster(t, "c", "hunter2-c"),
	}

	res, err := runContentComparison(t.Context(), results, &cfg, testNow)
	if err != nil || res.skipReason != "" {
		t.Fatalf("runContentComparison: %+v, %v", res, err)
	}
	findings := res.findings
	if len(findings) != 1 || findings[0].Clusters[0] != "c" {
		t.Fatalf("want one content-differs finding on c, got %+v", findings)
	}
	raw, _ := json.Marshal(findings)
	assertNoSecret(t, "findings JSON", string(raw), "hunter2-a", "hunter2-c")
	if !strings.Contains(string(raw), "hmac-sha256:") {
		t.Errorf("want keyed digests in the report, got %s", raw)
	}
}

// --why on a Secret whose copies differ shows the content difference with
// keyed digests only, never the value.
func TestWhy_SecretContentDiffersShowsNoValue(t *testing.T) {
	fakeFleet(t, secretCluster(t, "a", "hunter2-a"), secretCluster(t, "b", "hunter2-a"), secretCluster(t, "c", "hunter2-c"))
	t.Setenv("KUBE_OUTLIER_CONTENT__KINDS", "Secret")

	code, stdout, stderr := runCLI(t, "scan", "--compare-content", "--why", "Secret payments/db")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q), want 0", code, stderr)
	}
	assertNoSecret(t, "--why output", stdout, "hunter2-a", "hunter2-c")
	if !strings.Contains(stdout, "reported as content-differs") || !strings.Contains(stdout, "hmac-sha256:") {
		t.Errorf("want the content difference as keyed digests, got:\n%s", stdout)
	}
}

var fakeGVR = schema.GroupVersionResource{Group: "generators.external-secrets.io", Version: "v1alpha1", Resource: "fakes"}

var fakeKey = identity.ResourceKey{Group: "generators.external-secrets.io", Kind: "Fake", Namespace: "payments", Name: "db"}

// fakeCluster is one scanned cluster holding a custom resource that keeps a
// password in its spec, served through a fake dynamic client.
func fakeCluster(t *testing.T, cluster, password string) clusterResult {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "generators.external-secrets.io/v1alpha1",
		"kind":       "Fake",
		"metadata":   map[string]interface{}{"name": "db", "namespace": "payments"},
		"spec":       map[string]interface{}{"data": map[string]interface{}{"password": password}},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{fakeGVR: "FakeList"}, obj)
	return clusterResult{
		snap: &inventory.Snapshot{Cluster: cluster, Resources: []inventory.Resource{{
			Key:               fakeKey,
			Cluster:           cluster,
			ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply"}},
			CreationTimestamp: metav1.NewTime(testNow),
		}}},
		gvrs:    []discovery.GVRInfo{{GVR: fakeGVR, Kind: "Fake", Namespaced: true}},
		clients: &connect.Clients{Dynamic: dyn},
	}
}

// A kind listed in sensitive.kinds gets the Secret treatment in the content
// comparison, the one path that reads full objects: keyed digests only.
func TestSensitiveKind_ValuesNeverLeave(t *testing.T) {
	cfg := config.Default()
	cfg.Content.Kinds = []string{"generators.external-secrets.io/Fake"}
	cfg.Sensitive.Kinds = []string{"generators.external-secrets.io/Fake"}
	results := []clusterResult{
		fakeCluster(t, "a", "hunter2-a"),
		fakeCluster(t, "b", "hunter2-a"),
		fakeCluster(t, "c", "hunter2-c"),
	}

	res, err := runContentComparison(t.Context(), results, &cfg, testNow)
	if err != nil || res.skipReason != "" {
		t.Fatalf("runContentComparison: %+v, %v", res, err)
	}
	findings := res.findings
	if len(findings) != 1 || findings[0].Clusters[0] != "c" {
		t.Fatalf("want one content-differs finding on c, got %+v", findings)
	}
	raw, _ := json.Marshal(findings)
	assertNoSecret(t, "findings JSON", string(raw), "hunter2-a", "hunter2-c")
	if !strings.Contains(string(raw), "hmac-sha256:") || strings.Contains(string(raw), `"sha256:`) {
		t.Errorf("want only keyed digests in the report, got %s", raw)
	}

	// Without the sensitive.kinds entry the same kind is compared as-is,
	// which is what the setting exists to prevent.
	cfg.Sensitive.Kinds = nil
	plain, err := runContentComparison(t.Context(), results, &cfg, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(plain.findings); !strings.Contains(string(raw), "hunter2-c") {
		t.Errorf("control: want the plaintext without sensitive.kinds, got %s", raw)
	}
}
