package report_test

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
	"github.com/vibe-shift-coding/kube-outlier/internal/report"
)

func TestRenderTable_HeaderAndRows(t *testing.T) {
	r := &report.Report{
		Findings: []analyze.Finding{
			{
				Key:       identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "debug-dump"},
				Clusters:  []string{"cluster-eu1"},
				Ownership: &ownership.OwnershipVerdict{Owner: ownership.OwnerKubectl, KubectlWritten: true},
				Score: analyze.Score{
					Total: 80,
					Reasons: []analyze.ScoreReason{
						{Code: "no-pipeline-markers", Points: 35},
						{Code: "kubectl-manager", Points: 25},
						{Code: "last-applied", Points: 10},
						{Code: "age", Points: 10, Detail: "137d"},
					},
				},
				AgeDays: intp(137),
			},
		},
	}

	var buf bytes.Buffer
	if err := report.RenderTable(&buf, r); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	out := buf.String()

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want header + 1 row, got %d lines: %q", len(lines), out)
	}
	if !strings.Contains(lines[0], "KIND") || !strings.Contains(lines[0], "NAMESPACE/NAME") ||
		!strings.Contains(lines[0], "CLUSTER") || !strings.Contains(lines[0], "OWNER") ||
		!strings.Contains(lines[0], "AGE") || !strings.Contains(lines[0], "SCORE") || !strings.Contains(lines[0], "WHY") {
		t.Errorf("header missing a column: %q", lines[0])
	}
	row := lines[1]
	for _, want := range []string{"ConfigMap", "default/debug-dump", "cluster-eu1", "kubectl", "137d", "80", "no-pipeline-markers,kubectl-manager,last-applied,age"} {
		if !strings.Contains(row, want) {
			t.Errorf("row missing %q: %q", want, row)
		}
	}
}

// With enrichment applied a PRESENT-ON column appears after NAMESPACE/NAME,
// CLUSTER joins merged clusters, and reasonless (fixed-score) classes render
// their class name in WHY.
func TestRenderTable_EnrichedColumns(t *testing.T) {
	r := &report.Report{
		FleetPresence: report.FleetPresence{Applied: true, Source: "live"},
		Findings: []analyze.Finding{
			{
				Class:     analyze.ClassLikelyManual,
				Key:       identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "debug-dump"},
				Clusters:  []string{"c1", "c2"},
				Ownership: &ownership.OwnershipVerdict{Owner: ownership.OwnerKubectl, KubectlWritten: true},
				Score: analyze.Score{
					Total: 100, FleetModifier: 20, FleetApplied: true,
					Reasons: []analyze.ScoreReason{
						{Code: "no-pipeline-markers", Points: 35},
						{Code: "fleet-rare", Points: 20, Detail: "present on 2/12"},
					},
				},
				Presence: &analyze.Presence{PresentOn: []string{"c1", "c2"}, MissingFrom: []string{}, PresentCount: 2, FleetSize: 12, Ratio: 2.0 / 12},
				AgeDays:  intp(137),
			},
			{
				Class:     analyze.ClassMissingFromCluster,
				Key:       identity.ResourceKey{Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: "shop", Name: "default-deny"},
				Clusters:  []string{"c9"},
				Ownership: &ownership.OwnershipVerdict{Owner: ownership.OwnerUnknown},
				Score:     analyze.Score{Total: 50, FleetApplied: true, Reasons: []analyze.ScoreReason{}},
				Presence:  &analyze.Presence{PresentOn: []string{"c1", "c2", "c3"}, MissingFrom: []string{"c9"}, PresentCount: 3, FleetSize: 4, Ratio: 0.75},
				AgeDays:   intp(300),
			},
		},
	}

	var buf bytes.Buffer
	if err := report.RenderTable(&buf, r); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got %d lines: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "PRESENT-ON") {
		t.Errorf("enriched header must include PRESENT-ON: %q", lines[0])
	}
	for _, want := range []string{"2/12", "c1,c2", "fleet-rare"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row 1 missing %q: %q", want, lines[1])
		}
	}
	for _, want := range []string{"3/4", "c9", "missing-from-cluster"} {
		if !strings.Contains(lines[2], want) {
			t.Errorf("row 2 missing %q: %q", want, lines[2])
		}
	}
}

func TestRenderTable_HeaderAlwaysPrintedWhenEmpty(t *testing.T) {
	r := &report.Report{}

	var buf bytes.Buffer
	restore := captureStderr(t)
	if err := report.RenderTable(&buf, r); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	stderrOut := restore()

	if !strings.Contains(buf.String(), "KIND") {
		t.Errorf("header must always print, got %q", buf.String())
	}
	// The renderer never writes to stderr itself; cmd prints "No findings.".
	if stderrOut != "" {
		t.Errorf("stderr = %q, want nothing from the renderer", stderrOut)
	}
}

// OWNER: "+kubectl" only when it adds information; "-" when nothing was
// classified.
func TestOwnerCell(t *testing.T) {
	cases := []struct {
		v    *ownership.OwnershipVerdict
		want string
	}{
		{nil, "-"},
		{&ownership.OwnershipVerdict{Owner: ownership.OwnerKubectl, KubectlWritten: true}, "kubectl"},
		{&ownership.OwnershipVerdict{Owner: ownership.OwnerHelm, KubectlWritten: true}, "helm+kubectl"},
		{&ownership.OwnershipVerdict{Owner: ownership.OwnerHelm}, "helm"},
		{&ownership.OwnershipVerdict{Owner: ownership.OwnerUnknown}, "unknown"},
	}
	for _, c := range cases {
		if got := report.OwnerCell(c.v); got != c.want {
			t.Errorf("OwnerCell(%+v) = %q, want %q", c.v, got, c.want)
		}
	}
}

// Against a baseline the presence column counts baseline clusters, and says
// so in its header.
func TestRenderTable_BaselineHeader(t *testing.T) {
	r := &report.Report{FleetPresence: report.FleetPresence{Applied: true, Source: "baseline"}}
	var buf bytes.Buffer
	if err := report.RenderTable(&buf, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "IN-BASELINE") || strings.Contains(buf.String(), "PRESENT-ON") {
		t.Errorf("baseline header = %q, want IN-BASELINE", buf.String())
	}
}

func TestHumanizeAge(t *testing.T) {
	r := &report.Report{
		Findings: []analyze.Finding{
			{Key: identity.ResourceKey{Kind: "ConfigMap", Name: "a"}, AgeDays: intp(137), Score: analyze.Score{}},
			{Key: identity.ResourceKey{Kind: "ConfigMap", Name: "b"}, AgeDays: intp(730), Score: analyze.Score{}},
		},
	}
	var buf bytes.Buffer
	if err := report.RenderTable(&buf, r); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "137d") {
		t.Errorf("want 137d in output, got %q", out)
	}
	if !strings.Contains(out, "2y") {
		t.Errorf("want 2y in output, got %q", out)
	}
}

// captureStderr redirects os.Stderr for the duration of the test and returns
// a function that restores it and returns everything written.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	return func() string {
		_ = w.Close()
		os.Stderr = old
		out, _ := io.ReadAll(r)
		return string(out)
	}
}

func TestSkipSummaryLines_Namespace(t *testing.T) {
	rep := &report.Report{SkippedGVRs: []report.SkippedGVR{
		{Cluster: "c1", GVR: "v1/configmaps", Namespace: "team-b", Reason: "forbidden", Forbidden: true},
		{Cluster: "c1", GVR: "v1/secrets", Reason: "forbidden", Forbidden: true},
	}}
	got := report.SkipSummaryLines(rep)
	want := []string{
		"c1: v1/configmaps in namespace team-b forbidden, skipped",
		"c1: v1/secrets forbidden, skipped",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("SkipSummaryLines = %q, want %q", got, want)
	}
}

// Every listing of a cluster, or of one -n namespace, forbidden: one line
// with the likely fix instead of one line per type.
func TestSkipSummaryLines_AllForbiddenCollapses(t *testing.T) {
	forbiddenIn := func(cluster, ns string, gvrs ...string) []report.SkippedGVR {
		var out []report.SkippedGVR
		for _, g := range gvrs {
			out = append(out, report.SkippedGVR{Cluster: cluster, GVR: g, Namespace: ns, Reason: "forbidden", Forbidden: true})
		}
		return out
	}
	cases := []struct {
		name     string
		skipped  []report.SkippedGVR
		listings report.ListingCounts
		want     []string
	}{
		{
			name:     "cluster-wide, no -n",
			skipped:  forbiddenIn("c1", "", "v1/configmaps", "v1/secrets"),
			listings: report.ListingCounts{ClusterWide: 2},
			want:     []string{"c1: all 2 resource types forbidden cluster-wide, skipped; if you can read only some namespaces, scan them with -n <namespace>"},
		},
		{
			name:     "one namespace, with -n",
			skipped:  forbiddenIn("c1", "team-b", "v1/configmaps", "v1/secrets"),
			listings: report.ListingCounts{PerNamespace: map[string]int{"team-a": 2, "team-b": 2}},
			want:     []string{"c1: all 2 resource types in namespace team-b forbidden, skipped; does the namespace exist, and can this identity read it?"},
		},
		{
			name:     "cluster-scoped types with -n",
			skipped:  forbiddenIn("c1", "", "v1/namespaces", "storage.k8s.io/v1/storageclasses"),
			listings: report.ListingCounts{ClusterWide: 2, PerNamespace: map[string]int{"team-a": 5}},
			want:     []string{"c1: all 2 cluster-scoped resource types forbidden, skipped; leave out --include-cluster-scoped if this identity can't read them"},
		},
		{
			name:     "only some forbidden: one line each",
			skipped:  forbiddenIn("c1", "", "v1/secrets"),
			listings: report.ListingCounts{ClusterWide: 20},
			want:     []string{"c1: v1/secrets forbidden, skipped"},
		},
	}
	for _, c := range cases {
		rep := &report.Report{SkippedGVRs: c.skipped, Listings: map[string]report.ListingCounts{"c1": c.listings}}
		if got := report.SkipSummaryLines(rep); strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s: SkipSummaryLines = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNothingRead(t *testing.T) {
	skipped := []report.SkippedGVR{
		{Cluster: "c1", GVR: "v1/configmaps", Forbidden: true},
		{Cluster: "c1", GVR: "v1/secrets", Forbidden: true},
		{Cluster: "c1", GVR: "example.com/v1/*", Reason: "discovery failed"},
	}
	all := &report.Report{SkippedGVRs: skipped, Listings: map[string]report.ListingCounts{"c1": {ClusterWide: 2}}}
	if !report.NothingRead(all) {
		t.Error("every listing failed: NothingRead = false, want true")
	}
	some := &report.Report{SkippedGVRs: skipped, Listings: map[string]report.ListingCounts{"c1": {ClusterWide: 3}}}
	if report.NothingRead(some) {
		t.Error("one listing worked: NothingRead = true, want false")
	}
	if report.NothingRead(&report.Report{SkippedGVRs: skipped}) {
		t.Error("without Listings: NothingRead = true, want false")
	}
}

func TestIsPartial(t *testing.T) {
	if report.IsPartial(&report.Report{}) {
		t.Error("empty report is not partial")
	}
	for name, rep := range map[string]*report.Report{
		"unreachable cluster": {Fleet: report.FleetSummary{Clusters: []report.ClusterInfo{{Name: "c1", Reachable: false}}}},
		"skipped type":        {SkippedGVRs: []report.SkippedGVR{{Cluster: "c1", GVR: "v1/secrets"}}},
		"unread copy":         {SkippedObjects: []report.SkippedObject{{Cluster: "c1"}}},
	} {
		if !report.IsPartial(rep) {
			t.Errorf("%s: IsPartial = false, want true", name)
		}
	}
}

// Narrower than the table: WHY moves to its own indented line and the other
// columns stay aligned; wide enough, or width unknown, the table is unchanged.
func TestRenderTableWidth_Narrow(t *testing.T) {
	r := &report.Report{Findings: []analyze.Finding{
		{
			Key:       identity.ResourceKey{Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: "team-a", Name: "temp-allow-all"},
			Clusters:  []string{"c1"},
			Ownership: &ownership.OwnershipVerdict{Owner: ownership.OwnerKubectl, KubectlWritten: true},
			Score: analyze.Score{Total: 75, Reasons: []analyze.ScoreReason{
				{Code: "no-pipeline-markers"}, {Code: "kubectl-manager"}, {Code: "kind-weight"},
			}},
			AgeDays: intp(6),
		},
	}}
	var wide, unknown, narrow bytes.Buffer
	if err := report.RenderTableWidth(&wide, r, 200); err != nil {
		t.Fatal(err)
	}
	if err := report.RenderTable(&unknown, r); err != nil {
		t.Fatal(err)
	}
	if wide.String() != unknown.String() {
		t.Errorf("a wide terminal must not change the table:\n%s\nvs\n%s", wide.String(), unknown.String())
	}
	if err := report.RenderTableWidth(&narrow, r, 80); err != nil {
		t.Fatal(err)
	}
	want := "SCORE  KIND                             NAMESPACE/NAME         CLUSTER  OWNER    AGE\n" +
		"75     networking.k8s.io/NetworkPolicy  team-a/temp-allow-all  c1       kubectl  6d\n" +
		"       why: no-pipeline-markers, kubectl-manager, kind-weight\n"
	if narrow.String() != want {
		t.Errorf("narrow table =\n%s\nwant\n%s", narrow.String(), want)
	}
}

func intp(v int) *int { return &v }
