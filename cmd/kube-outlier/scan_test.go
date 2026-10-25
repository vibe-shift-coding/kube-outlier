package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/baseline"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/connect"
	"github.com/vibe-shift-coding/kube-outlier/internal/discovery"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
)

// scan --help must document every scan flag, and none of the removed ones.
func TestScan_HelpDocumentsFlags(t *testing.T) {
	code, stdout, _ := runCLI(t, "scan", "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, flag := range []string{"--include-cluster-scoped", "--namespaces", "--fail-on", "--output", "--min-score", "--baseline", "--allow-scope-mismatch", "--why", "--compare-content", "--allow-partial", "--request-timeout", "--contexts", "--kubeconfig", "--config"} {
		if !strings.Contains(stdout, flag) {
			t.Errorf("scan --help missing %s:\n%s", flag, stdout)
		}
	}
	for _, flag := range []string{"--hide-pipeline-owned", "--only-rare", "--export-manifests", "--save-inventory", "--from-inventory"} {
		if strings.Contains(stdout, flag) {
			t.Errorf("scan --help still lists the removed %s", flag)
		}
	}
	if !strings.Contains(stdout, `--fail-on string`) || !strings.Contains(stdout, `(default "none")`) {
		t.Errorf("scan --help must show the --fail-on default:\n%s", stdout)
	}
}

// version needs no cluster, so its help lists no cluster flags.
func TestVersion_HelpListsNoClusterFlags(t *testing.T) {
	_, stdout, _ := runCLI(t, "version", "--help")
	for _, flag := range []string{"--contexts", "--kubeconfig", "--probe-timeout", "--config"} {
		if strings.Contains(stdout, flag) {
			t.Errorf("version --help lists %s:\n%s", flag, stdout)
		}
	}
}

// The generated completion command works but is not advertised.
func TestCompletion_Hidden(t *testing.T) {
	_, stdout, _ := runCLI(t, "--help")
	if strings.Contains(stdout, "completion") {
		t.Errorf("root help lists the completion command:\n%s", stdout)
	}
	if code, out, _ := runCLI(t, "completion", "bash"); code != 0 || out == "" {
		t.Errorf("completion bash: exit=%d, want a script", code)
	}
}

func TestParseWhyKey(t *testing.T) {
	tests := []struct {
		in   string
		want identity.ResourceKey
		err  bool
	}{
		{in: "ConfigMap default/debug-dump", want: identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "debug-dump"}},
		{in: "apps/Deployment shop/api", want: identity.ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "api"}},
		{in: "rbac.authorization.k8s.io/ClusterRole admin", want: identity.ResourceKey{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "admin"}},
		{in: "ConfigMap", err: true},
		{in: "a/b/c x", err: true},
		{in: "ConfigMap ns/", err: true},
		{in: "", err: true},
	}
	for _, tt := range tests {
		got, err := parseWhyKey(tt.in)
		if tt.err {
			if err == nil {
				t.Errorf("%q: want error, got %+v", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%q: got %+v (%v), want %+v", tt.in, got, err, tt.want)
		}
	}
}

var testNow = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

var (
	cmGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	npGVR = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}
)

// fakeFleet makes every command in this test see results as its scanned
// clusters, all reachable, instead of connecting anywhere.
func fakeFleet(t *testing.T, results ...clusterResult) {
	t.Helper()
	orig := collectFleet
	t.Cleanup(func() { collectFleet = orig })
	collectFleet = func(context.Context, *config.Config) ([]connect.ClusterRef, []clusterResult, error) {
		refs := make([]connect.ClusterRef, 0, len(results))
		for _, r := range results {
			refs = append(refs, connect.ClusterRef{Name: r.snap.Cluster, Context: r.snap.Cluster, Reachable: true})
		}
		return refs, results, nil
	}
}

// oneOffCluster is a cluster holding one hand-applied, 137-day-old ConfigMap
// default/oops (base score 80).
func oneOffCluster(name string) clusterResult {
	return clusterResult{
		snap: &inventory.Snapshot{Cluster: name, Resources: []inventory.Resource{{
			Key:               identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "oops"},
			Cluster:           name,
			APIVersion:        "v1",
			Annotations:       map[string]string{"kubectl.kubernetes.io/last-applied-configuration": ""},
			ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply"}},
			CreationTimestamp: metav1.NewTime(testNow.AddDate(0, 0, -137)),
		}}},
		gvrs: []discovery.GVRInfo{{GVR: cmGVR, Kind: "ConfigMap", Namespaced: true}},
	}
}

// helmPolicyCluster is a cluster that may hold the Helm-managed
// NetworkPolicy default/deny.
func helmPolicyCluster(name string, withPolicy bool) clusterResult {
	r := clusterResult{
		snap: &inventory.Snapshot{Cluster: name},
		gvrs: []discovery.GVRInfo{{GVR: npGVR, Kind: "NetworkPolicy", Namespaced: true}},
	}
	if withPolicy {
		r.snap.Resources = []inventory.Resource{{
			Key:               identity.ResourceKey{Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: "default", Name: "deny"},
			Cluster:           name,
			Labels:            map[string]string{"app.kubernetes.io/managed-by": "Helm"},
			ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "helm"}},
			CreationTimestamp: metav1.NewTime(testNow),
		}}
	}
	return r
}

// A scan reports and gates without any live cluster behind it.
func TestScan_ReportAndGate(t *testing.T) {
	fakeFleet(t, oneOffCluster("cached"))

	code, stdout, stderr := runCLI(t, "scan", "--fail-on", "inventory", "-o", "json")
	if code != 3 {
		t.Fatalf("exit = %d (stderr %q), want 3", code, stderr)
	}
	for _, want := range []string{`"name": "oops"`, `"source": "none"`, `"reachable": 1`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("json missing %s:\n%s", want, stdout)
		}
	}
}

func TestScan_Why(t *testing.T) {
	fakeFleet(t, oneOffCluster("cached"))

	code, stdout, stderr := runCLI(t, "scan", "--why", "ConfigMap default/oops")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q), want 0", code, stderr)
	}
	for _, want := range []string{
		"ConfigMap default/oops",
		"status: reported (score 80 >= minimum score 40)",
		"owner: kubectl",
		"kubectl-client-side-apply",
		"no-pipeline-markers",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--why missing %q:\n%s", want, stdout)
		}
	}

	code, _, stderr = runCLI(t, "scan", "--why", "ConfigMap default/nope")
	if code != 1 || !strings.Contains(stderr, "not found in scope") {
		t.Errorf("unknown identity: exit=%d stderr=%q, want exit 1 not-found", code, stderr)
	}

	code, _, stderr = runCLI(t, "scan", "--why", "ConfigMap default/oops", "-o", "json")
	if code != 1 || !strings.Contains(stderr, "text only") {
		t.Errorf("--why -o json: exit=%d stderr=%q, want exit 1", code, stderr)
	}

	// A report format from the environment (or a config file) is for
	// reports; --why still renders its text view.
	t.Setenv("KUBE_OUTLIER_REPORT__FORMAT", "json")
	code, stdout, stderr = runCLI(t, "scan", "--why", "ConfigMap default/oops")
	if code != 0 || !strings.Contains(stdout, "status: reported") {
		t.Errorf("--why with report.format json from env: exit=%d stderr=%q stdout=%q, want the text view", code, stderr, stdout)
	}
}

// --why names a missing-from-cluster finding instead of only the resource's
// own (low) likely-manual score, so it agrees with the report.
func TestScan_WhyMissingFromCluster(t *testing.T) {
	fakeFleet(t, helmPolicyCluster("a", true), helmPolicyCluster("b", true), helmPolicyCluster("c", false))

	code, stdout, stderr := runCLI(t, "scan", "--why", "networking.k8s.io/NetworkPolicy default/deny")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q), want 0", code, stderr)
	}
	for _, want := range []string{
		"status: reported as missing-from-cluster: missing from c (present on 2/3 clusters)",
		"not reported as likely-manual: below the minimum score",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--why missing %q:\n%s", want, stdout)
		}
	}
}

// writeTestBaseline writes an artifact whose fingerprint matches
// config.Default().Scope, with one ubiquitous ConfigMap entry, one rare one,
// and one active exception, and returns its path.
func writeTestBaseline(t *testing.T) string {
	t.Helper()
	entries := []baseline.Entry{
		{
			Key:              baseline.ResourceRef{Kind: "ConfigMap", Namespace: "default", Name: "everywhere"},
			ClusterCount:     3,
			Ratio:            1.0,
			OwnershipSummary: map[ownership.Owner]int{ownership.OwnerHelm: 3},
		},
		{
			Key:              baseline.ResourceRef{Kind: "ConfigMap", Namespace: "default", Name: "elsewhere"},
			ClusterCount:     1,
			Ratio:            1.0 / 3,
			OwnershipSummary: map[ownership.Owner]int{ownership.OwnerKubectl: 1},
		},
	}
	exceptions := []baseline.ApprovedException{{
		Key:        baseline.ResourceRef{Kind: "ConfigMap", Namespace: "default", Name: "excused"},
		Reason:     "known one-off",
		ApprovedBy: "tests",
	}}
	b := baseline.New("v-test", testNow, []string{"a", "b", "c"},
		baseline.FingerprintScope(config.Default().Scope), entries, exceptions)

	path := filepath.Join(t.TempDir(), "fleet.yaml")
	var buf bytes.Buffer
	if err := b.Write(&buf, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func targetResults() []clusterResult {
	snap := &inventory.Snapshot{Cluster: "d", Resources: []inventory.Resource{
		{
			Key:               identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "oops"},
			Annotations:       map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}"},
			ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply"}},
			CreationTimestamp: metav1.NewTime(testNow.AddDate(0, 0, -137)),
		},
		{
			Key:               identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "excused"},
			Annotations:       map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}"},
			ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply"}},
			CreationTimestamp: metav1.NewTime(testNow.AddDate(0, 0, -137)),
		},
	}}
	gvrs := []discovery.GVRInfo{{GVR: cmGVR, Kind: "ConfigMap", Namespaced: true}}
	return []clusterResult{{snap: snap, gvrs: gvrs}}
}

func TestBaselineScan(t *testing.T) {
	cfg := config.Default()
	base, err := loadBaselineContext(writeTestBaseline(t), false, &cfg)
	if err != nil {
		t.Fatalf("loadBaselineContext: %v", err)
	}

	findings, suppressed, byException := baselineScan(io.Discard, base, targetResults(), &cfg, testNow)
	if byException != 1 {
		t.Errorf("byException = %d, want 1 (excused)", byException)
	}
	if suppressed != 0 {
		t.Errorf("suppressed = %d, want 0", suppressed)
	}

	var names []string
	for _, f := range findings {
		names = append(names, string(f.Class)+":"+f.Key.Name)
	}
	// oops: 80 + fleet-rare 20 = 100; everywhere: ratio 1.0, absent -> missing-from.
	want := []string{"likely-manual:oops", "missing-from-cluster:everywhere"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("findings = %v, want %v", names, want)
	}
	for _, f := range findings {
		if f.Key.Name == "oops" && (f.Score.Total != 100 || !f.Score.FleetApplied) {
			t.Errorf("oops score = %+v, want 100 with fleet applied", f.Score)
		}
	}
}

// Against a baseline, --why explains resources the scanned cluster lacks:
// a reported absence, and an absence too rare to report.
func TestScan_WhyAgainstBaseline(t *testing.T) {
	fakeFleet(t, targetResults()...)
	path := writeTestBaseline(t)

	code, stdout, stderr := runCLI(t, "scan", "--baseline", path, "--why", "ConfigMap default/everywhere")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q), want 0", code, stderr)
	}
	for _, want := range []string{
		"status: reported as missing-from-cluster: missing from d (present on 3/3 clusters)",
		"not present on any scanned cluster",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--why missing %q:\n%s", want, stdout)
		}
	}

	code, stdout, stderr = runCLI(t, "scan", "--baseline", path, "--why", "ConfigMap default/elsewhere")
	if code != 0 || !strings.Contains(stdout, "the baseline has it on 1/3 clusters, below analysis.commonThreshold") {
		t.Errorf("rare baseline entry: exit=%d stderr=%q stdout:\n%s", code, stderr, stdout)
	}

	code, stdout, _ = runCLI(t, "scan", "--baseline", path, "--why", "ConfigMap default/excused")
	if code != 0 || !strings.Contains(stdout, "status: suppressed by approved exception (approved by tests: known one-off)") ||
		!strings.Contains(stdout, "without the exception: reported (score 100") {
		t.Errorf("excepted resource: exit=%d stdout:\n%s", code, stdout)
	}
}

func TestBaselineScan_ScopeMismatch(t *testing.T) {
	path := writeTestBaseline(t)
	cfg := config.Default()
	cfg.Scope.IncludeClusterScoped = true // diverge from the artifact's fingerprint

	_, err := loadBaselineContext(path, false, &cfg)
	var ee *exitError
	if err == nil || !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("want exit-1 error on scope mismatch, got %v", err)
	}
	if !strings.Contains(err.Error(), "scope mismatch") {
		t.Errorf("error = %q, want it to name the scope mismatch", err)
	}

	// --allow-scope-mismatch downgrades to a warning and proceeds.
	base, err := loadBaselineContext(path, true, &cfg)
	if err != nil {
		t.Fatalf("allowMismatch=true must proceed, got %v", err)
	}
	if findings, _, _ := baselineScan(io.Discard, base, targetResults(), &cfg, testNow); len(findings) == 0 {
		t.Error("allowMismatch=true must still produce findings")
	}
}

func TestBaselineScan_BadArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("schemaVersion: nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	_, err := loadBaselineContext(path, false, &cfg)
	var ee *exitError
	if err == nil || !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("want exit-1 error on unloadable artifact, got %v", err)
	}
}

// fail-on content without the comparison enabled could never fire, so it is fatal rather
// than a silently passing CI gate; fail-on any still gates on inventory findings.
func TestScan_FailOnContentNeedsCompareContent(t *testing.T) {
	fakeFleet(t, oneOffCluster("cached"))

	code, _, stderr := runCLI(t, "scan", "--fail-on", "content")
	if code != 1 || !strings.Contains(stderr, "needs --compare-content") {
		t.Errorf("fail-on content without --compare-content: exit=%d stderr=%q, want exit 1", code, stderr)
	}

	code, _, stderr = runCLI(t, "scan", "--fail-on", "any")
	if code != 3 {
		t.Errorf("fail-on any without --compare-content: exit=%d stderr=%q, want 3 (inventory still gates)", code, stderr)
	}

	// One cluster: the comparison cannot run, so the gate is fatal too.
	code, _, stderr = runCLI(t, "scan", "--compare-content", "--fail-on", "content")
	if code != 1 || !strings.Contains(stderr, "could not run") {
		t.Errorf("one-cluster content gate: exit=%d stderr=%q, want exit 1", code, stderr)
	}

	// --why ignores failOn entirely, so it is not rejected there.
	code, _, stderr = runCLI(t, "scan", "--fail-on", "content", "--why", "ConfigMap default/oops")
	if code != 0 {
		t.Errorf("--why with fail-on content: exit=%d stderr=%q, want 0", code, stderr)
	}

	t.Setenv("KUBE_OUTLIER_REPORT__FAILON", "content")
	code, _, stderr = runCLI(t, "scan")
	if code != 1 || !strings.Contains(stderr, "needs --compare-content") {
		t.Errorf("report.failOn content without compare-content: exit=%d stderr=%q, want exit 1", code, stderr)
	}
}

// A partial run exits 2 unless --allow-partial / report.allowPartial is set;
// failOn triggers still win.
func TestScan_AllowPartial(t *testing.T) {
	partial := oneOffCluster("cached")
	partial.snap.Resources = nil
	partial.snap.Errors = []inventory.GVRError{{
		GVR:       schema.GroupVersionResource{Version: "v1", Resource: "secrets"},
		Err:       "forbidden",
		Forbidden: true,
	}}
	fakeFleet(t, partial)

	if code, _, stderr := runCLI(t, "scan"); code != 2 {
		t.Errorf("partial run: exit=%d stderr=%q, want 2", code, stderr)
	}
	if code, _, stderr := runCLI(t, "scan", "--allow-partial"); code != 0 {
		t.Errorf("partial run with --allow-partial: exit=%d stderr=%q, want 0", code, stderr)
	}
	t.Setenv("KUBE_OUTLIER_REPORT__ALLOWPARTIAL", "true")
	if code, _, stderr := runCLI(t, "scan"); code != 0 {
		t.Errorf("partial run with report.allowPartial env: exit=%d stderr=%q, want 0", code, stderr)
	}
}

func TestBaselineGenerate_HelpDocumentsFlags(t *testing.T) {
	code, stdout, _ := runCLI(t, "baseline", "generate", "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, flag := range []string{"--out", "--namespaces", "--include-cluster-scoped", "--discard-exceptions", "--contexts"} {
		if !strings.Contains(stdout, flag) {
			t.Errorf("baseline generate --help missing %s:\n%s", flag, stdout)
		}
	}
	for _, flag := range []string{"--omit-cluster-names", "--content-hashes"} {
		if strings.Contains(stdout, flag) {
			t.Errorf("baseline generate --help still lists the removed %s", flag)
		}
	}
}

// A namespace named with -n that holds nothing on a cluster that could list
// it is almost certainly a typo, and says so; a namespace the identity could
// not read is already reported as skipped.
func TestWarnEmptyNamespaces(t *testing.T) {
	r := oneOffCluster("c1")
	r.snap.Errors = []inventory.GVRError{{GVR: cmGVR, Namespace: "locked", Forbidden: true}}
	cfg := config.Default()
	cfg.Scope.Namespaces.Include = []string{"default", "locked", "typo"}

	var buf bytes.Buffer
	warnEmptyNamespaces(&buf, []clusterResult{r}, &cfg)
	if got, want := buf.String(), "warning: c1: nothing found in namespace typo; does it exist?\n"; got != want {
		t.Errorf("warnings = %q, want %q", got, want)
	}
}

// By default only warnings reach stderr, in the CLI's own "warning:" form.
func TestWarningHandler(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(warningHandler{w: &buf}).With("cluster", "c1")
	logger.Info("progress")
	logger.Warn("context defined twice", "context", "a")
	logger.Error("boom")
	want := "warning: context defined twice cluster=c1 context=a\nerror: boom cluster=c1\n"
	if buf.String() != want {
		t.Errorf("output = %q, want %q", buf.String(), want)
	}
}

// --why on a resource whose copies differ names the content difference and
// shows its field changes.
func TestBuildWhy_ContentDiffers(t *testing.T) {
	key := identity.ResourceKey{Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: "default", Name: "deny"}
	results := []clusterResult{helmPolicyCluster("a", true), helmPolicyCluster("b", true), helmPolicyCluster("c", true)}
	cfg := config.Default()
	differs := analyze.Finding{
		Class:    analyze.ClassContentDiffers,
		Key:      key,
		Clusters: []string{"c"},
		Content:  &analyze.ContentDetail{MajorityClusters: []string{"a", "b"}},
	}

	e, err := buildWhy(key, whyInputs{results: results, cfg: &cfg, source: "live", reported: []analyze.Finding{differs}, now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "reported as content-differs: the copy on c differs from the majority version" {
		t.Errorf("status = %q", e.Status)
	}
	if len(e.Content) != 1 {
		t.Errorf("content = %+v, want the content-differs finding", e.Content)
	}
}

// kubectl's singular spellings work as aliases.
func TestFlagAliases(t *testing.T) {
	code, _, stderr := runCLI(t, "scan",
		"--context", "nope",
		"--namespace", "payments",
		"--kubeconfig", "../../testdata/kubeconfig/unreachable.yaml",
	)
	if code != 1 || strings.Contains(stderr, "unknown flag") || !strings.Contains(stderr, `unknown context "nope"`) {
		t.Errorf("--context/--namespace aliases: exit=%d stderr=%q, want the unknown-context error", code, stderr)
	}
}

// -o belongs to the commands that render a report; elsewhere it is an
// error rather than silently ignored.
func TestOutputFlagOnlyWhereRendered(t *testing.T) {
	if code, _, stderr := runCLI(t, "baseline", "generate", "-o", "json"); code != 1 || !strings.Contains(stderr, "unknown shorthand flag") {
		t.Errorf("baseline generate -o: exit=%d stderr=%q, want an unknown-flag error", code, stderr)
	}
	if code, _, stderr := runCLI(t, "version", "-o", "json"); code != 1 || !strings.Contains(stderr, "unknown shorthand flag") {
		t.Errorf("version -o: exit=%d stderr=%q, want an unknown-flag error", code, stderr)
	}
}

// A content comparison with one cluster cannot run; callers turn a content
// gate on it into a fatal error.
func TestScan_ContentGateNeedsTheComparisonToRun(t *testing.T) {
	cfg := config.Default()
	cfg.Report.FailOn = config.FailOnContent
	res, err := runContentComparison(t.Context(), targetResults(), &cfg, testNow)
	if err != nil || res.skipReason == "" || len(res.findings) != 0 {
		t.Fatalf("one cluster: result=%+v err=%v, want skipped", res, err)
	}
}

// All four declared formats render; formats outside the enum are
// still rejected by config validation before touching any cluster.
func TestScan_FormatAcceptance(t *testing.T) {
	// yaml/markdown are accepted as formats: the run proceeds past format
	// validation and fails later on the unknown context instead.
	for _, format := range []string{"yaml", "markdown"} {
		code, _, stderr := runCLI(t, "scan",
			"--contexts", "nope",
			"--kubeconfig", "../../testdata/kubeconfig/unreachable.yaml",
			"-o", format,
		)
		if code != 1 || strings.Contains(stderr, "not supported") {
			t.Errorf("format %q must be accepted (exit=%d stderr=%q)", format, code, stderr)
		}
		if !strings.Contains(stderr, "nope") {
			t.Errorf("format %q: expected the unknown-context error, got %q", format, stderr)
		}
	}

	code, _, stderr := runCLI(t, "scan",
		"--contexts", "nope",
		"--kubeconfig", "../../testdata/kubeconfig/unreachable.yaml",
		"-o", "xml",
	)
	if code != 1 || !strings.Contains(stderr, "xml") {
		t.Errorf("format xml: exit=%d stderr=%q, want exit 1 naming the format", code, stderr)
	}
}
