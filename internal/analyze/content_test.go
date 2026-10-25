package analyze_test

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
)

func cb(cluster, hash string, body map[string]interface{}) analyze.CanonicalBody {
	return analyze.CanonicalBody{Cluster: cluster, Hash: hash, Body: body}
}

func depBody(image string) map[string]interface{} {
	return map[string]interface{}{
		"kind": "Deployment",
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{"name": "api", "image": image},
					},
				},
			},
		},
	}
}

var depKey = identity.ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "api"}

func presenceOf(clusters ...string) map[identity.ResourceKey]*analyze.Presence {
	return map[identity.ResourceKey]*analyze.Presence{
		depKey: {PresentOn: clusters, MissingFrom: []string{}, PresentCount: len(clusters), FleetSize: len(clusters), Ratio: 1.0},
	}
}

func creationOf(clusters ...string) map[identity.ResourceKey]map[string]metav1.Time {
	m := map[string]metav1.Time{}
	for _, c := range clusters {
		m[c] = metav1.NewTime(fixedNow.AddDate(0, 0, -10))
	}
	return map[identity.ResourceKey]map[string]metav1.Time{depKey: m}
}

// One differing cluster against a clear majority → one content-differs finding with the
// image replace op, consensus and divergent lists correct.
func TestContentFindings_ImageDiffers(t *testing.T) {
	bodies := map[identity.ResourceKey][]analyze.CanonicalBody{
		depKey: {
			cb("a", "h1", depBody("api:v1")),
			cb("b", "h1", depBody("api:v1")),
			cb("c", "h2", depBody("api:v2")),
		},
	}
	findings := analyze.ContentFindings(bodies, presenceOf("a", "b", "c"), creationOf("a", "b", "c"), fixedNow)

	if len(findings) != 1 {
		t.Fatalf("want 1 content-differs finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Class != analyze.ClassContentDiffers || f.Score.Total != 0 {
		t.Errorf("class/score = %v/%d, want content-differs/0", f.Class, f.Score.Total)
	}
	// reasons is an array on every finding, never null (interfaces.md §2.6).
	if f.Score.Reasons == nil || len(f.Score.Reasons) != 0 {
		t.Errorf("score.reasons = %#v, want an empty, non-nil slice", f.Score.Reasons)
	}
	if len(f.Clusters) != 1 || f.Clusters[0] != "c" {
		t.Errorf("divergent clusters = %v, want [c]", f.Clusters)
	}
	d := f.Content
	if d == nil {
		t.Fatalf("content detail = %+v", d)
	}
	if len(d.MajorityClusters) != 2 || d.MajorityClusters[0] != "a" || d.MajorityClusters[1] != "b" {
		t.Errorf("consensus = %v, want [a b]", d.MajorityClusters)
	}
	if len(d.Differing) != 1 || d.Differing[0].Cluster != "c" {
		t.Fatalf("divergent = %+v", d.Differing)
	}
	ops := d.Differing[0].Ops
	if len(ops) != 1 || ops[0].Op != "replace" {
		t.Fatalf("ops = %+v, want one replace", ops)
	}
	if ops[0].Path != "/spec/template/spec/containers/0/image" {
		t.Errorf("op path = %q", ops[0].Path)
	}
	if ops[0].MajorityValue != "api:v1" || ops[0].ClusterValue != "api:v2" {
		t.Errorf("op values = %v -> %v, want api:v1 -> api:v2", ops[0].MajorityValue, ops[0].ClusterValue)
	}
}

// Full agreement → no finding.
func TestContentFindings_AllMatch(t *testing.T) {
	bodies := map[identity.ResourceKey][]analyze.CanonicalBody{
		depKey: {
			cb("a", "h1", depBody("api:v1")),
			cb("b", "h1", depBody("api:v1")),
		},
	}
	if got := analyze.ContentFindings(bodies, presenceOf("a", "b"), creationOf("a", "b"), fixedNow); len(got) != 0 {
		t.Errorf("want no finding, got %+v", got)
	}
}

// Tie for largest group → deterministic winner (lexicographically-first
// cluster list) and Ambiguous flagged.
func TestContentFindings_AmbiguousTie(t *testing.T) {
	bodies := map[identity.ResourceKey][]analyze.CanonicalBody{
		depKey: {
			cb("z", "h2", depBody("api:v2")),
			cb("a", "h1", depBody("api:v1")),
		},
	}
	findings := analyze.ContentFindings(bodies, presenceOf("a", "z"), creationOf("a", "z"), fixedNow)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(findings))
	}
	d := findings[0].Content
	if !d.Ambiguous {
		t.Error("want Ambiguous=true for a 1-1 split")
	}
	// Group {a} sorts before {z} → consensus is {a}, divergent {z}.
	if len(d.MajorityClusters) != 1 || d.MajorityClusters[0] != "a" {
		t.Errorf("consensus = %v, want [a] (lexicographic tie-break)", d.MajorityClusters)
	}
	if findings[0].Clusters[0] != "z" {
		t.Errorf("divergent = %v, want [z]", findings[0].Clusters)
	}
}

// The largest group is only a "majority" when it holds more than half the
// copies: 2 of 5 is a plurality, so the finding is marked ambiguous.
func TestContentFindings_PluralityIsAmbiguous(t *testing.T) {
	bodies := map[identity.ResourceKey][]analyze.CanonicalBody{depKey: {
		cb("a", "h1", depBody("api:v1")),
		cb("b", "h1", depBody("api:v1")),
		cb("c", "h2", depBody("api:v2")),
		cb("d", "h3", depBody("api:v3")),
		cb("e", "h4", depBody("api:v4")),
	}}
	findings := analyze.ContentFindings(bodies, nil, nil, fixedNow)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(findings))
	}
	if !findings[0].Content.Ambiguous {
		t.Error("2 of 5 is not a majority: want Ambiguous")
	}
	if !strings.Contains(findings[0].Message, "most common version (no majority)") {
		t.Errorf("Message = %q, want it to say there is no majority", findings[0].Message)
	}

	// 3 of 5 is a majority.
	bodies[depKey][2] = cb("c", "h1", depBody("api:v1"))
	findings = analyze.ContentFindings(bodies, nil, nil, fixedNow)
	if findings[0].Content.Ambiguous {
		t.Error("3 of 5 is a majority: want not ambiguous")
	}
}
