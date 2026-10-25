package analyze_test

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parsing time %q: %v", s, err)
	}
	return tm
}

func TestTier1Findings(t *testing.T) {
	cfg := config.Default().Analysis // MinScore 40

	snap := &inventory.Snapshot{
		Cluster: "c1",
		Resources: []inventory.Resource{
			{
				Key:               identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "debug-dump"},
				Annotations:       map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}"},
				ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply"}},
				CreationTimestamp: metav1.NewTime(mustParse(t, "2026-02-14T09:30:00Z")),
			},
			{
				Key:               identity.ResourceKey{Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: "default", Name: "temp-allow-all"},
				ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "kubectl-create"}},
				CreationTimestamp: metav1.NewTime(mustParse(t, "2026-06-28T16:12:55Z")),
			},
			{
				Key: identity.ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "ingress-nginx", Name: "ingress-nginx-controller"},
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "Helm",
				},
				Annotations:       map[string]string{"meta.helm.sh/release-name": "ingress-nginx"},
				ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "helm"}},
				CreationTimestamp: metav1.NewTime(mustParse(t, "2025-11-12T08:14:03Z")),
			},
			{
				Key:               identity.ResourceKey{Kind: "ConfigMap", Namespace: "search", Name: "search-es-scripts"},
				OwnerReferences:   []metav1.OwnerReference{{Kind: "Elasticsearch", Name: "search-es"}},
				ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "elastic-operator"}},
				CreationTimestamp: metav1.NewTime(mustParse(t, "2026-03-03T10:05:41Z")),
			},
		},
	}

	findings, suppressed := analyze.Tier1Findings(snap, cfg, fixedNow)

	if suppressed != 1 {
		t.Fatalf("suppressed = %d, want 1 (the Helm Deployment scoring 18)", suppressed)
	}
	if len(findings) != 2 {
		t.Fatalf("len(findings) = %d, want 2: %+v", len(findings), findings)
	}

	if findings[0].Key.Name != "debug-dump" || findings[0].Score.Total != 80 {
		t.Errorf("findings[0] = %+v, want debug-dump @ 80", findings[0])
	}
	if findings[1].Key.Name != "temp-allow-all" || findings[1].Score.Total != 75 {
		t.Errorf("findings[1] = %+v, want temp-allow-all @ 75", findings[1])
	}

	for _, f := range findings {
		if f.Class != analyze.ClassLikelyManual {
			t.Errorf("Class = %v, want %v", f.Class, analyze.ClassLikelyManual)
		}
		if len(f.Clusters) != 1 || f.Clusters[0] != "c1" {
			t.Errorf("Clusters = %v, want [c1]", f.Clusters)
		}
		if f.Presence != nil {
			t.Errorf("Presence must be nil for single-cluster findings, got %+v", f.Presence)
		}
		if _, ok := f.CreationTimestamps["c1"]; !ok {
			t.Errorf("CreationTimestamps missing cluster c1: %+v", f.CreationTimestamps)
		}
		if f.Message == "" || !containsAll(f.Message, f.Key.Kind, "written by a kubectl client") {
			t.Errorf("Message = %q, want it to name the kind and the kubectl write", f.Message)
		}
	}
}

// A resource with Helm markers that a kubectl client also wrote is described
// as exactly that, never as "not pipeline-managed".
func TestTier1Findings_MessageHelmPlusKubectl(t *testing.T) {
	cfg := config.Default().Analysis
	cfg.MinScore = 0
	r := inventory.Resource{
		Key:               identity.ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"},
		Labels:            map[string]string{"app.kubernetes.io/managed-by": "Helm"},
		ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "helm"}, {Manager: "kubectl-edit"}},
		CreationTimestamp: metav1.NewTime(fixedNow),
	}
	findings, _ := analyze.Tier1Findings(&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{r}}, cfg, fixedNow)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", findings)
	}
	want := "apps/Deployment default/web carries Helm markers but was also written by a kubectl client"
	if findings[0].Message != want {
		t.Errorf("Message = %q, want %q", findings[0].Message, want)
	}
}

func TestTier1Findings_MessageVariants(t *testing.T) {
	cfg := config.Default().Analysis

	kubectlWritten := inventory.Resource{
		Key:               identity.ResourceKey{Kind: "Secret", Namespace: "default", Name: "human"},
		ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "kubectl-create"}},
		CreationTimestamp: metav1.NewTime(fixedNow.AddDate(0, 0, -200)),
	}
	notKubectlWritten := inventory.Resource{
		Key:               identity.ResourceKey{Kind: "Secret", Namespace: "default", Name: "unknown-op"},
		ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "some-operator"}},
		CreationTimestamp: metav1.NewTime(fixedNow.AddDate(0, 0, -200)),
	}
	snap := &inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{kubectlWritten, notKubectlWritten}}

	findings, _ := analyze.Tier1Findings(snap, cfg, fixedNow)
	if len(findings) != 2 {
		t.Fatalf("len(findings) = %d, want 2: %+v", len(findings), findings)
	}
	byName := map[string]analyze.Finding{}
	for _, f := range findings {
		byName[f.Key.Name] = f
	}
	if !containsAll(byName["human"].Message, "written by a kubectl client", "no pipeline markers") {
		t.Errorf("kubectl-written message = %q, want it to say written by a kubectl client", byName["human"].Message)
	}
	if !containsAll(byName["unknown-op"].Message, "no recognized pipeline ownership") {
		t.Errorf("non-human message = %q, want it to say no recognized pipeline ownership", byName["unknown-op"].Message)
	}
}

func TestTier1Findings_SystemIgnored(t *testing.T) {
	cfg := config.Default().Analysis

	// Both Services are unmanaged with no ownership markers, so each scores
	// unmanaged (35) + Service kind-weight (8) = 43, above the default
	// threshold of 40. The apiserver's default/kubernetes Service must be
	// excluded outright (built-in system ignore); the identically-shaped
	// default/other-svc must still be flagged — the ignore is identity-exact,
	// not kind- or namespace-wide.
	snap := &inventory.Snapshot{
		Cluster: "c1",
		Resources: []inventory.Resource{
			{
				Key:               identity.ResourceKey{Kind: "Service", Namespace: "default", Name: "kubernetes"},
				CreationTimestamp: metav1.NewTime(fixedNow),
			},
			{
				Key:               identity.ResourceKey{Kind: "Service", Namespace: "default", Name: "other-svc"},
				CreationTimestamp: metav1.NewTime(fixedNow),
			},
		},
	}

	findings, suppressed := analyze.Tier1Findings(snap, cfg, fixedNow)

	// The ignored resource is out of scope entirely — not a finding, and not
	// counted as suppressed-below-threshold (same treatment as owner-referenced).
	if suppressed != 0 {
		t.Errorf("suppressed = %d, want 0 (kubernetes Service is ignored, not suppressed)", suppressed)
	}
	if len(findings) != 1 {
		t.Fatalf("len(findings) = %d, want 1 (only other-svc): %+v", len(findings), findings)
	}
	if findings[0].Key.Name != "other-svc" {
		t.Errorf("findings[0] = %+v, want the non-ignored other-svc", findings[0])
	}
	if findings[0].Score.Total != 43 {
		t.Errorf("other-svc Score.Total = %d, want 43 (unmanaged 35 + Service kind-weight 8)", findings[0].Score.Total)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
