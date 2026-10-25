package report_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/baseline"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/connect"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
	"github.com/vibe-shift-coding/kube-outlier/internal/normalize"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
	"github.com/vibe-shift-coding/kube-outlier/internal/report"
	"github.com/vibe-shift-coding/kube-outlier/internal/testutil"
)

var update = flag.Bool("update", false, "regenerate golden files instead of comparing against them")

const fixturesDir = "../../testdata/fixtures"

var goldenNow = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

// TestGoldenPipeline is the Tier 1 end-to-end check: fixtures -> Tier1Findings ->
// Build -> RenderJSON/RenderTable, byte-compared against the committed
// goldens. Run with -update to regenerate them after an intentional change;
// the diff must be reviewed by a human before committing.
func TestGoldenPipeline(t *testing.T) {
	resources := testutil.LoadFixtures(t, fixturesDir)
	snap := &inventory.Snapshot{Cluster: "fixture", Resources: resources}

	cfg := config.Default()

	findings, suppressed := analyze.Tier1Findings(snap, cfg.Analysis, goldenNow)

	// Structural assertions, independent of the golden byte-compare.
	if len(findings) != 2 {
		t.Fatalf("want exactly 2 findings, got %d: %+v", len(findings), findings)
	}
	if findings[0].Key.Kind != "ConfigMap" || findings[0].Key.Name != "debug-dump" || findings[0].Score.Total != 80 {
		t.Errorf("findings[0] = %+v, want ConfigMap default/debug-dump @ 80", findings[0])
	}
	if findings[1].Key.Kind != "NetworkPolicy" || findings[1].Key.Name != "temp-allow-all" || findings[1].Score.Total != 75 {
		t.Errorf("findings[1] = %+v, want NetworkPolicy default/temp-allow-all @ 75", findings[1])
	}
	for _, f := range findings {
		if f.Ownership.Owner == "helm" || f.Ownership.Owner == "argocd" || f.Ownership.Owner == "controller" {
			t.Errorf("helm/argo/controller fixtures must not be findings, got %+v", f)
		}
	}

	refs := []connect.ClusterRef{
		{Name: "fixture", Context: "fixture", Reachable: true, ServerVersion: "v1.30.0"},
	}
	rep := report.Build(refs, cfg.Scope, []*inventory.Snapshot{snap}, findings, suppressed,
		report.FleetPresence{Applied: false, Source: "none"}, "v0.1.0-test", goldenNow)
	rep.Partial = report.IsPartial(rep)
	rep.ExitCode = report.ExitCode(rep, config.ReportConfig{FailOn: config.FailOnNone})

	var jsonBuf, tableBuf bytes.Buffer
	if err := report.RenderJSON(&jsonBuf, rep); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if err := report.RenderTable(&tableBuf, rep); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}

	compareGolden(t, "../../testdata/golden/scan-fixtures.json", jsonBuf.Bytes())
	compareGolden(t, "../../testdata/golden/scan-fixtures.table.txt", tableBuf.Bytes())
}

// TestGoldenFleetPipeline is the fleet end-to-end check: three synthetic cluster
// snapshots -> Tier2Findings -> Build -> RenderJSON/RenderTable, byte-compared
// against committed goldens. It exercises both fleet outcomes at once:
//   - a kubectl one-off on cluster a only -> fleet-rare, promoted to 100;
//   - an un-annotated resource on all three -> fleet-common, demoted below
//     threshold (suppressed);
//   - a Helm resource on a+b (r=2/3 >= commonThreshold 0.6) -> one
//     missing-from-cluster finding for c;
//   - a Widget CRD served on a+b only -> no finding (a type is not a finding).
func mkRes(t *testing.T, key identity.ResourceKey, created string, mut func(*inventory.Resource)) inventory.Resource {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, created)
	if err != nil {
		t.Fatalf("parsing %q: %v", created, err)
	}
	r := inventory.Resource{Key: key, CreationTimestamp: metav1.NewTime(ts)}
	if mut != nil {
		mut(&r)
	}
	return r
}

// kubectlConfigMap is a hand-applied ConfigMap (kubectl apply: last-applied
// annotation + kubectl manager).
func kubectlConfigMap(t *testing.T, name, created string) inventory.Resource {
	return mkRes(t, identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: name},
		created, func(r *inventory.Resource) {
			r.Annotations = map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}"}
			r.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply"}}
		})
}

// operatorConfigMap is un-annotated with a non-kubectl manager.
func operatorConfigMap(t *testing.T, name, created string) inventory.Resource {
	return mkRes(t, identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: name},
		created, func(r *inventory.Resource) {
			r.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "bootstrap-operator"}}
		})
}

// helmDeployment carries full Helm markers and the helm field manager.
func helmDeployment(t *testing.T, name, created string) inventory.Resource {
	return mkRes(t, identity.ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "default", Name: name},
		created, func(r *inventory.Resource) {
			r.Labels = map[string]string{"app.kubernetes.io/managed-by": "Helm"}
			r.Annotations = map[string]string{"meta.helm.sh/release-name": name}
			r.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "helm"}}
		})
}

// fleetTrioSnapshots is the shared fleet/baseline fixture fleet: a kubectl one-off on
// a only, an un-annotated ConfigMap on all three, a Helm Deployment on a+b.
func fleetTrioSnapshots(t *testing.T) []*inventory.Snapshot {
	return []*inventory.Snapshot{
		{Cluster: "a", Resources: []inventory.Resource{
			kubectlConfigMap(t, "debug-dump", "2026-02-14T09:30:00Z"),
			operatorConfigMap(t, "cluster-info", "2025-12-13T00:00:00Z"),
			helmDeployment(t, "ingress", "2025-11-12T08:14:03Z"),
		}},
		{Cluster: "b", Resources: []inventory.Resource{
			operatorConfigMap(t, "cluster-info", "2025-12-13T00:00:00Z"),
			helmDeployment(t, "ingress", "2025-11-12T08:14:03Z"),
		}},
		{Cluster: "c", Resources: []inventory.Resource{
			operatorConfigMap(t, "cluster-info", "2025-12-13T00:00:00Z"),
		}},
	}
}

func TestGoldenFleetPipeline(t *testing.T) {
	snaps := fleetTrioSnapshots(t)

	baseKinds := []config.KindRef{{Kind: "ConfigMap"}, {Group: "apps", Kind: "Deployment"}}
	widget := config.KindRef{Group: "acme.example.com", Kind: "Widget"}
	served := analyze.ServedKinds{}
	failed := analyze.ServedKinds{}
	for _, s := range snaps {
		set := map[config.KindRef]bool{}
		for _, kr := range baseKinds {
			set[kr] = true
		}
		if s.Cluster != "c" {
			set[widget] = true
		}
		served[s.Cluster] = set
		failed[s.Cluster] = map[config.KindRef]bool{}
	}

	cfg := config.Default()
	cfg.Analysis.CommonThreshold = 0.6

	fleet := analyze.FleetInputs{Matrix: analyze.BuildPresence(snaps), Served: served, Failed: failed}
	findings, suppressed := analyze.Tier2Findings(snaps, fleet, cfg.Analysis, goldenNow)

	// Structural assertions, independent of the golden byte-compare.
	if len(findings) != 2 {
		t.Fatalf("want exactly 2 findings, got %d: %+v", len(findings), findings)
	}
	if findings[0].Class != analyze.ClassLikelyManual || findings[0].Key.Name != "debug-dump" || findings[0].Score.Total != 100 {
		t.Errorf("findings[0] = %+v, want debug-dump likely-manual @ 100", findings[0])
	}
	// cluster-info: 35 + age(10) = 45 base, -30 ubiquitous = 15 -> suppressed
	// on all three clusters; ingress @ 18 suppressed on a and b.
	if suppressed != 5 {
		t.Errorf("suppressed = %d, want 5 (cluster-info x3 + ingress x2)", suppressed)
	}

	refs := []connect.ClusterRef{
		{Name: "a", Context: "a", Reachable: true, ServerVersion: "v1.30.0"},
		{Name: "b", Context: "b", Reachable: true, ServerVersion: "v1.30.0"},
		{Name: "c", Context: "c", Reachable: true, ServerVersion: "v1.30.0"},
	}
	rep := report.Build(refs, cfg.Scope, snaps, findings, suppressed,
		report.FleetPresence{Applied: true, Source: "live"}, "v0.1.0-test", goldenNow)
	rep.Partial = report.IsPartial(rep)
	rep.ExitCode = report.ExitCode(rep, config.ReportConfig{FailOn: config.FailOnNone})

	var jsonBuf, tableBuf bytes.Buffer
	if err := report.RenderJSON(&jsonBuf, rep); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if err := report.RenderTable(&tableBuf, rep); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}

	compareGolden(t, "../../testdata/golden/scan-fleet.json", jsonBuf.Bytes())
	compareGolden(t, "../../testdata/golden/scan-fleet.table.txt", tableBuf.Bytes())

	// Same report through the yaml and markdown renderers.
	var yamlBuf, mdBuf bytes.Buffer
	if err := report.RenderYAML(&yamlBuf, rep); err != nil {
		t.Fatalf("RenderYAML: %v", err)
	}
	if err := report.RenderMarkdown(&mdBuf, rep); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	compareGolden(t, "../../testdata/golden/scan-fleet.yaml", yamlBuf.Bytes())
	compareGolden(t, "../../testdata/golden/scan-fleet.md", mdBuf.Bytes())
}

// TestGoldenExplain locks the --why rendering: a fleet-context
// likely-manual resource with full evidence.
func TestGoldenExplain(t *testing.T) {
	e := report.Explain{
		Key:    identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "debug-dump"},
		Status: "reported (score 100 >= minimum score 40)",
		Source: "live",
		Occurrences: []report.ExplainOccurrence{{
			Cluster: "a",
			Ownership: ownership.OwnershipVerdict{
				Owner:          ownership.OwnerKubectl,
				KubectlWritten: true,
				Managers:       []string{"kubectl-client-side-apply"},
				Signals: []ownership.Signal{
					{Code: "kubectl.manager", Detail: "manager=kubectl-client-side-apply"},
					{Code: "kubectl.last-applied", Detail: "kubectl.kubernetes.io/last-applied-configuration present"},
					{Code: "no-pipeline-markers", Detail: "no Helm, Argo CD or Flux markers, no ownerReferences"},
				},
			},
			CreationTimestamp: metav1.NewTime(time.Date(2026, 2, 14, 9, 30, 0, 0, time.UTC)),
			AgeDays:           137,
		}},
		Score: &analyze.Score{
			Base: 80, FleetModifier: 20, Total: 100, FleetApplied: true,
			Reasons: []analyze.ScoreReason{
				{Code: "no-pipeline-markers", Points: 35},
				{Code: "kubectl-manager", Points: 25},
				{Code: "last-applied", Points: 10},
				{Code: "age", Points: 10, Detail: "137d"},
				{Code: "fleet-rare", Points: 20, Detail: "present on 1/3"},
			},
		},
		Presence: &analyze.Presence{PresentOn: []string{"a"}, MissingFrom: []string{"b", "c"}, PresentCount: 1, FleetSize: 3, Ratio: 1.0 / 3},
	}

	var buf bytes.Buffer
	if err := report.RenderExplain(&buf, e); err != nil {
		t.Fatalf("RenderExplain: %v", err)
	}
	compareGolden(t, "../../testdata/golden/explain.txt", buf.Bytes())
}

// TestGoldenBaselinePipeline is the baseline end-to-end check. It regenerates the
// committed baseline artifact from the shared fleet trio (golden-compared, so
// the fixture can never drift from the code that would produce it), then runs
// a synthetic 4th cluster "d" against the LOADED fixture:
//   - a fresh kubectl one-off unknown to the baseline -> r=0, fleet-rare, 100;
//   - cluster-info (baseline ratio 1.0, present locally) -> demoted 45-30=15,
//     suppressed;
//   - ingress (ratio 2/3 >= commonThreshold 0.6, absent locally) -> one
//     missing-from-cluster finding;
//   - known-oneoff (96) suppressed by the active approved exception;
//   - the expired exception is ignored.
func TestGoldenBaselinePipeline(t *testing.T) {
	cfg := config.Default()

	// 1. The artifact, regenerated from the trio and byte-compared.
	snaps := fleetTrioSnapshots(t)
	matrix := analyze.BuildPresence(snaps)
	tallies := analyze.OwnershipTallies(snaps, cfg.Analysis)
	var entries []baseline.Entry
	for key := range matrix.ByKey {
		count := len(matrix.PresentOn(key))
		entries = append(entries, baseline.Entry{
			Key:              baseline.ResourceRef(key),
			ClusterCount:     count,
			Ratio:            float64(count) / 3.0,
			OwnershipSummary: tallies[key],
		})
	}
	expiry := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exceptions := []baseline.ApprovedException{
		{
			Key:        baseline.ResourceRef{Kind: "ConfigMap", Namespace: "default", Name: "known-oneoff"},
			Reason:     "sanctioned debug ConfigMap, tracked in PLAT-1234",
			ApprovedBy: "platform-team",
		},
		{
			Key:        baseline.ResourceRef{Kind: "ConfigMap", Namespace: "default", Name: "old-exception"},
			Reason:     "expired example",
			ApprovedBy: "platform-team",
			Expires:    &expiry,
		},
	}
	art := baseline.New("v0.1.0-test", goldenNow, []string{"a", "b", "c"},
		baseline.FingerprintScope(cfg.Scope), entries, exceptions)
	var artBuf bytes.Buffer
	if err := art.Write(&artBuf, false); err != nil {
		t.Fatalf("Write: %v", err)
	}
	compareGolden(t, "../../testdata/baseline/fleet.yaml", artBuf.Bytes())

	// 2. Scan cluster d against the LOADED committed fixture.
	loaded, err := baseline.Load("../../testdata/baseline/fleet.yaml")
	if err != nil {
		t.Fatalf("loading committed fixture: %v", err)
	}
	ref := analyze.BaselineRef{
		FleetSize: loaded.Provenance.ClusterCount,
		Clusters:  loaded.Provenance.Clusters,
		Entries:   map[identity.ResourceKey]analyze.BaselineEntryRef{},
		Kinds:     map[config.KindRef]bool{},
	}
	for _, e := range loaded.Entries {
		ref.Entries[e.Key.ResourceKey()] = analyze.BaselineEntryRef{ClusterCount: e.ClusterCount, Ratio: e.Ratio}
		ref.Kinds[config.KindRef{Group: e.Key.Group, Kind: e.Key.Kind}] = true
	}

	target := &inventory.Snapshot{Cluster: "d", Resources: []inventory.Resource{
		operatorConfigMap(t, "cluster-info", "2025-12-13T00:00:00Z"),
		kubectlConfigMap(t, "incident-dump", "2026-02-14T09:30:00Z"),
		kubectlConfigMap(t, "known-oneoff", "2026-05-01T00:00:00Z"),
	}}
	served := map[config.KindRef]bool{{Kind: "ConfigMap"}: true, {Group: "apps", Kind: "Deployment"}: true}

	cfgA := cfg.Analysis
	cfgA.CommonThreshold = 0.6
	findings, suppressed := analyze.BaselineFindings(target, ref, served, map[config.KindRef]bool{}, cfgA, goldenNow)

	// Exception filtering mirrors cmd's baselineScan (unit-tested there);
	// the golden covers the rendered result.
	active, expiredEx := loaded.ActiveExceptions(goldenNow)
	if len(expiredEx) != 1 || expiredEx[0].Key.Name != "old-exception" {
		t.Fatalf("expired exceptions = %+v, want [old-exception]", expiredEx)
	}
	byException := 0
	kept := findings[:0]
	for _, f := range findings {
		if _, ok := active[f.Key]; ok {
			byException++
			continue
		}
		kept = append(kept, f)
	}

	// Structural assertions, independent of the golden byte-compare.
	if byException != 1 {
		t.Errorf("suppressedByException = %d, want 1 (known-oneoff)", byException)
	}
	if suppressed != 1 {
		t.Errorf("suppressed = %d, want 1 (cluster-info demoted 45-30=15)", suppressed)
	}
	if len(kept) != 2 {
		t.Fatalf("want exactly 2 findings, got %d: %+v", len(kept), kept)
	}
	if kept[0].Key.Name != "incident-dump" || kept[0].Score.Total != 100 {
		t.Errorf("kept[0] = %+v, want incident-dump @ 100", kept[0])
	}
	if kept[1].Class != analyze.ClassMissingFromCluster || kept[1].Key.Name != "ingress" || kept[1].Score.Total != 50 {
		t.Errorf("kept[1] = %+v, want ingress missing-from-cluster @ 50", kept[1])
	}

	refs := []connect.ClusterRef{{Name: "d", Context: "d", Reachable: true, ServerVersion: "v1.30.0"}}
	rep := report.Build(refs, cfg.Scope, []*inventory.Snapshot{target}, kept, suppressed,
		report.FleetPresence{Applied: true, Source: "baseline"}, "v0.1.0-test", goldenNow)
	rep.Summary.SuppressedByException = byException
	rep.Partial = report.IsPartial(rep)
	rep.ExitCode = report.ExitCode(rep, config.ReportConfig{FailOn: config.FailOnNone})

	var jsonBuf, tableBuf bytes.Buffer
	if err := report.RenderJSON(&jsonBuf, rep); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if err := report.RenderTable(&tableBuf, rep); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	compareGolden(t, "../../testdata/golden/scan-baseline.json", jsonBuf.Bytes())
	compareGolden(t, "../../testdata/golden/scan-baseline.table.txt", tableBuf.Bytes())
}

// TestGoldenContentPipeline is the content-comparison end-to-end check: three clusters run a
// Deployment identical on a,b and image-skewed on c → one content-differs finding
// (consensus {a,b}, divergent c, one image replace op), rendered to
// json/table/markdown. Exercises the full normalize → consensus → jsondiff →
// render path.
func TestGoldenContentPipeline(t *testing.T) {
	depKey := identity.ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"}
	deploy := func(image string) map[string]interface{} {
		return map[string]interface{}{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]interface{}{"name": "web", "namespace": "default"},
			"spec": map[string]interface{}{
				"replicas": float64(2),
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"schedulerName": "default-scheduler", // stripped → not a difference
						"containers": []interface{}{
							map[string]interface{}{"name": "web", "image": image},
						},
					},
				},
			},
		}
	}
	mk := func(cluster, image string) analyze.CanonicalBody {
		body := normalize.CanonicalizeForComparison(deploy(image), depKey, nil, false)
		hash, err := normalize.CanonicalHash(body)
		if err != nil {
			t.Fatal(err)
		}
		return analyze.CanonicalBody{Cluster: cluster, Hash: hash, Body: body}
	}

	bodies := map[identity.ResourceKey][]analyze.CanonicalBody{
		depKey: {mk("a", "web:v1"), mk("b", "web:v1"), mk("c", "web:v2")},
	}
	presence := map[identity.ResourceKey]*analyze.Presence{
		depKey: {PresentOn: []string{"a", "b", "c"}, MissingFrom: []string{}, PresentCount: 3, FleetSize: 3, Ratio: 1.0},
	}
	creation := map[identity.ResourceKey]map[string]metav1.Time{
		depKey: {
			"a": metav1.NewTime(goldenNow.AddDate(0, 0, -30)),
			"b": metav1.NewTime(goldenNow.AddDate(0, 0, -30)),
			"c": metav1.NewTime(goldenNow.AddDate(0, 0, -5)),
		},
	}

	findings := analyze.ContentFindings(bodies, presence, creation, goldenNow)

	// Structural assertions, independent of the golden byte-compare.
	if len(findings) != 1 {
		t.Fatalf("want 1 content-differs finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Class != analyze.ClassContentDiffers || f.Content == nil {
		t.Fatalf("finding = %+v, want a content-differs finding", f)
	}
	if len(f.Content.Differing) != 1 || f.Content.Differing[0].Cluster != "c" {
		t.Errorf("divergent = %+v, want [c]", f.Content.Differing)
	}
	ops := f.Content.Differing[0].Ops
	if len(ops) != 1 || ops[0].Path != "/spec/template/spec/containers/0/image" ||
		ops[0].MajorityValue != "web:v1" || ops[0].ClusterValue != "web:v2" {
		t.Fatalf("ops = %+v, want one image replace v1→v2", ops)
	}

	refs := []connect.ClusterRef{
		{Name: "a", Context: "a", Reachable: true, ServerVersion: "v1.30.0"},
		{Name: "b", Context: "b", Reachable: true, ServerVersion: "v1.30.0"},
		{Name: "c", Context: "c", Reachable: true, ServerVersion: "v1.30.0"},
	}
	rep := report.Build(refs, config.Default().Scope, nil, findings, 0,
		report.FleetPresence{Applied: true, Source: "live"}, "v0.1.0-test", goldenNow)
	rep.Partial = report.IsPartial(rep)
	rep.ExitCode = report.ExitCode(rep, config.ReportConfig{FailOn: config.FailOnContent})
	if rep.ExitCode != 4 {
		t.Errorf("exit code = %d, want 4 (content-differs, failOn content)", rep.ExitCode)
	}

	var jsonBuf, tableBuf, mdBuf bytes.Buffer
	if err := report.RenderJSON(&jsonBuf, rep); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if err := report.RenderTable(&tableBuf, rep); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	if err := report.RenderMarkdown(&mdBuf, rep); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	compareGolden(t, "../../testdata/golden/scan-content.json", jsonBuf.Bytes())
	compareGolden(t, "../../testdata/golden/scan-content.table.txt", tableBuf.Bytes())
	compareGolden(t, "../../testdata/golden/scan-content.md", mdBuf.Bytes())
}

func compareGolden(t *testing.T, path string, got []byte) {
	t.Helper()

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("writing golden file %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file %s (run with -update to create it): %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("golden mismatch for %s:\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}
