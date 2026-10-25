package main

import (
	"bytes"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/vibe-shift-coding/kube-outlier/internal/baseline"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/discovery"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
	"github.com/vibe-shift-coding/kube-outlier/internal/report"
)

// servedTypes is what a typical cluster serves: core ConfigMaps, Secrets and
// Pods, Deployments, NetworkPolicies, and the cluster-scoped ClusterRole.
var servedTypes = []discovery.GVRInfo{
	{GVR: cmGVR, Kind: "ConfigMap", Namespaced: true},
	{GVR: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, Kind: "Secret", Namespaced: true},
	{GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Kind: "Pod", Namespaced: true},
	{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Kind: "Deployment", Namespaced: true},
	{GVR: npGVR, Kind: "NetworkPolicy", Namespaced: true},
	{GVR: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, Kind: "ClusterRole"},
}

// withServed adds the discovery data a live scan has to a test cluster.
func withServed(r clusterResult) clusterResult {
	r.served = servedTypes
	return r
}

// --why names the likely cause when it finds nothing.
func TestWhy_NotFoundHints(t *testing.T) {
	cfg := config.Default()
	in := whyInputs{results: []clusterResult{withServed(oneOffCluster("c1"))}, cfg: &cfg, source: "none", now: testNow}
	cases := map[string]string{
		"NetworkPolicy default/deny":                          `did you mean "networking.k8s.io/NetworkPolicy default/deny"?`,
		"configmap default/oops":                              `did you mean "ConfigMap default/oops"?`,
		"example.com/Widget default/w":                        "no scanned cluster serves the kind example.com/Widget",
		"rbac.authorization.k8s.io/ClusterRole admin":         "rbac.authorization.k8s.io/ClusterRole is cluster-scoped; add --include-cluster-scoped",
		"rbac.authorization.k8s.io/ClusterRole default/admin": `is cluster-scoped: write it without a namespace, as "rbac.authorization.k8s.io/ClusterRole admin"`,
		"ConfigMap oops":                                      `ConfigMap is namespaced: write it as "ConfigMap <namespace>/oops"`,
		"ConfigMap kube-system/coredns":                       "namespace kube-system is excluded by default; scan it with -n kube-system",
		"Pod default/web-0":                                   "Pod is never scanned (a built-in exclusion)",
	}
	for arg, want := range cases {
		key, err := identity.ParseKey(arg)
		if err != nil {
			t.Fatal(err)
		}
		_, err = buildWhy(key, in)
		if err == nil || !strings.Contains(err.Error(), "not found in scope") || !strings.Contains(err.Error(), want) {
			t.Errorf("--why %q: error = %v, want it to say %q", arg, err, want)
		}
	}

	// Nothing the scan can explain: no hint, just not found.
	key, _ := identity.ParseKey("ConfigMap default/missing")
	if _, err := buildWhy(key, in); err == nil || err.Error() != "ConfigMap default/missing not found in scope on any scanned cluster" {
		t.Errorf("error = %v, want the plain not-found message", err)
	}

	// With -n, a namespace outside the list is named.
	cfg.Scope.Namespaces.Include = []string{"team-a"}
	key, _ = identity.ParseKey("ConfigMap team-b/x")
	if _, err := buildWhy(key, in); err == nil || !strings.Contains(err.Error(), "namespace team-b is not among the -n namespaces") {
		t.Errorf("error = %v, want the -n hint", err)
	}
}

// A scope.kinds entry that matches nothing served is reported, with the
// probable spelling; matching and built-in entries are not.
func TestWarnUnmatchedKinds(t *testing.T) {
	cfg := config.Default()
	cfg.Scope.Kinds.Exclude = append(cfg.Scope.Kinds.Exclude, "NetworkPolicy", "Secret", "apps/Deploymnt")
	var buf bytes.Buffer
	warnUnmatchedKinds(&buf, []clusterResult{withServed(oneOffCluster("c1"))}, &cfg)
	got := buf.String()
	for _, want := range []string{
		`warning: scope.kinds.exclude: "NetworkPolicy" matches no resource type on any scanned cluster (did you mean "networking.k8s.io/NetworkPolicy"?)`,
		`warning: scope.kinds.exclude: "apps/Deploymnt" matches no resource type on any scanned cluster`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, `"Secret"`) || strings.Contains(got, "Event") || strings.Count(got, "\n") != 2 {
		t.Errorf("warnings = %q, want exactly the two unmatched entries", got)
	}

	// Without discovery data there is nothing to check against.
	buf.Reset()
	warnUnmatchedKinds(&buf, []clusterResult{oneOffCluster("c1")}, &cfg)
	if buf.Len() != 0 {
		t.Errorf("without discovery data: warnings = %q, want none", buf.String())
	}
}

// A content.kinds entry without its group compares nothing, so with no entry
// matching, a content gate is refused instead of passing forever.
func TestScan_ContentGateWithNoMatchingKind(t *testing.T) {
	fakeFleet(t, withServed(oneOffCluster("c1")), withServed(oneOffCluster("c2")))
	t.Setenv("KUBE_OUTLIER_CONTENT__KINDS", "Deployment")
	code, _, stderr := runCLI(t, "scan", "--compare-content", "--fail-on", "content")
	if code != 1 || !strings.Contains(stderr, "no content.kinds entry matches a resource type the scan listed") {
		t.Errorf("exit = %d, stderr %q: want 1 and the reason", code, stderr)
	}

	var buf bytes.Buffer
	cfg := config.Default()
	cfg.Content.Kinds = []string{"Deployment", "ConfigMap", "Secret"}
	cfg.Sensitive.Kinds = []string{"Fake"}
	matched := contentKindsInScope(&buf, []clusterResult{withServed(oneOffCluster("c1"))}, &cfg)
	if matched != 1 {
		t.Errorf("matched = %d, want 1 (ConfigMap)", matched)
	}
	for _, want := range []string{
		`content.kinds: "Deployment" matches no resource type on any scanned cluster (did you mean "apps/Deployment"?)`,
		`content.kinds: "Secret" is outside the scan scope`,
		`sensitive.kinds: "Fake" matches no resource type on any scanned cluster (kinds outside the core group need their group`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("warnings = %q, want %q", buf.String(), want)
		}
	}
}

// An exception that names nothing the baseline or the scan has is reported,
// with the identity it probably meant.
func TestWarnUnknownExceptions(t *testing.T) {
	cfg := config.Default()
	base, err := loadBaselineContext(writeTestBaseline(t), false, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	np := identity.ResourceKey{Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: "default", Name: "deny"}
	results := []clusterResult{helmPolicyCluster("c1", true)}
	typo := identity.ResourceKey{Kind: "NetworkPolicy", Namespace: "default", Name: "deny"}
	active := map[identity.ResourceKey]baseline.ApprovedException{
		typo: {Key: baseline.ResourceRef(typo), Reason: "r", ApprovedBy: "a"},
		np:   {Key: baseline.ResourceRef(np), Reason: "r", ApprovedBy: "a"},
	}
	var buf bytes.Buffer
	warnUnknownExceptions(&buf, base, active, results)
	want := `warning: baseline exception "NetworkPolicy default/deny" matches no resource in the baseline or on the scanned cluster(s) (did you mean "networking.k8s.io/NetworkPolicy default/deny"?)` + "\n"
	if buf.String() != want {
		t.Errorf("warnings = %q, want %q", buf.String(), want)
	}
}

func TestListingCounts(t *testing.T) {
	r := clusterResult{snap: &inventory.Snapshot{Cluster: "c1"}, gvrs: servedTypes}
	cfg := config.Default()
	if got := listingCounts([]clusterResult{r}, &cfg)["c1"]; got.ClusterWide != 6 || got.PerNamespace != nil {
		t.Errorf("without -n: %+v, want 6 cluster-wide listings", got)
	}
	cfg.Scope.Namespaces.Include = []string{"a", "b"}
	got := listingCounts([]clusterResult{r}, &cfg)["c1"]
	want := report.ListingCounts{ClusterWide: 1, PerNamespace: map[string]int{"a": 5, "b": 5}}
	if got.ClusterWide != want.ClusterWide || got.PerNamespace["a"] != 5 || got.PerNamespace["b"] != 5 {
		t.Errorf("with -n: %+v, want %+v", got, want)
	}
}

// The table's summary goes to stderr; with nothing readable at all, "No
// findings" says so.
func TestScan_TableSummary(t *testing.T) {
	fakeFleet(t, oneOffCluster("c1"))
	code, stdout, stderr := runCLI(t, "scan")
	if code != 0 || !strings.HasPrefix(stdout, "SCORE") || !strings.Contains(stderr, "1 finding(s), 0 below the minimum score.") {
		t.Errorf("exit %d\nstdout %q\nstderr %q", code, stdout, stderr)
	}

	blind := clusterResult{
		snap: &inventory.Snapshot{Cluster: "c1", Errors: []inventory.GVRError{{GVR: cmGVR, Err: "forbidden", Forbidden: true}}},
		gvrs: []discovery.GVRInfo{{GVR: cmGVR, Kind: "ConfigMap", Namespaced: true}},
	}
	fakeFleet(t, blind)
	code, _, stderr = runCLI(t, "scan")
	if code != 2 || !strings.Contains(stderr, "No findings — nothing could be read") {
		t.Errorf("nothing readable: exit %d, stderr %q", code, stderr)
	}
}

// A flag mistake says where the usage is, since usage itself is silenced.
func TestFlagErrorPointsToHelp(t *testing.T) {
	code, _, stderr := runCLI(t, "scan", "--namespace-include", "x")
	if code != 1 || !strings.Contains(stderr, "unknown flag: --namespace-include") || !strings.Contains(stderr, "Run 'kube-outlier scan --help' for usage.") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}
