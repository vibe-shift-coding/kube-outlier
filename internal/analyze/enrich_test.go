package analyze_test

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
)

// kubectlRes is an unmanaged, kubectl-applied ConfigMap: base score 35+25+10
// + age. Created 137 days before fixedNow → base 80.
func kubectlRes(name string) inventory.Resource {
	return inventory.Resource{
		Key:               identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: name},
		Annotations:       map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}"},
		ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply"}},
		CreationTimestamp: metav1.NewTime(fixedNow.AddDate(0, 0, -137)),
	}
}

// unmanagedRes is an unmanaged ConfigMap with no kubectl evidence and age
// < 7d: base score 35 (below the default threshold of 40 on its own).
func unmanagedRes(name string) inventory.Resource {
	return inventory.Resource{
		Key:               identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: name},
		ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "some-operator"}},
		CreationTimestamp: metav1.NewTime(fixedNow.AddDate(0, 0, -3)),
	}
}

// helmRes is a Helm-owned Deployment, 200d old: base score 8+10 = 18.
func helmRes(name string) inventory.Resource {
	return inventory.Resource{
		Key:    identity.ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "default", Name: name},
		Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"},
		Annotations: map[string]string{
			"meta.helm.sh/release-name": name,
		},
		ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "helm"}},
		CreationTimestamp: metav1.NewTime(fixedNow.AddDate(0, 0, -200)),
	}
}

// unmanagedSecretRes is an unmanaged Secret, 45d old, no kubectl evidence:
// base 35+15+6 = 56.
func unmanagedSecretRes(name string) inventory.Resource {
	return inventory.Resource{
		Key:               identity.ResourceKey{Kind: "Secret", Namespace: "default", Name: name},
		ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "some-operator"}},
		CreationTimestamp: metav1.NewTime(fixedNow.AddDate(0, 0, -45)),
	}
}

// fleetOf builds snapshots plus FleetInputs where every cluster serves every
// kind appearing anywhere in the fleet, with no listing failures.
func fleetOf(snaps ...*inventory.Snapshot) ([]*inventory.Snapshot, analyze.FleetInputs) {
	allKinds := map[config.KindRef]bool{}
	for _, s := range snaps {
		for _, r := range s.Resources {
			allKinds[config.KindRef{Group: r.Key.Group, Kind: r.Key.Kind}] = true
		}
	}
	served := analyze.ServedKinds{}
	failed := analyze.ServedKinds{}
	for _, s := range snaps {
		perCluster := make(map[config.KindRef]bool, len(allKinds))
		for kr := range allKinds {
			perCluster[kr] = true
		}
		served[s.Cluster] = perCluster
		failed[s.Cluster] = map[config.KindRef]bool{}
	}
	return snaps, analyze.FleetInputs{
		Matrix: analyze.BuildPresence(snaps),
		Served: served,
		Failed: failed,
	}
}

func findByName(findings []analyze.Finding, class analyze.FindingClass, name string) *analyze.Finding {
	for i := range findings {
		if findings[i].Class == class && findings[i].Key.Name == name {
			return &findings[i]
		}
	}
	return nil
}

func TestBuildPresence(t *testing.T) {
	snaps := []*inventory.Snapshot{
		{Cluster: "b", Resources: []inventory.Resource{kubectlRes("only-b"), unmanagedRes("both")}},
		{Cluster: "a", Resources: []inventory.Resource{unmanagedRes("both")}},
	}
	m := analyze.BuildPresence(snaps)

	if len(m.Clusters) != 2 || m.Clusters[0] != "a" || m.Clusters[1] != "b" {
		t.Fatalf("Clusters = %v, want sorted [a b]", m.Clusters)
	}
	both := identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "both"}
	onlyB := identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "only-b"}
	if got := m.PresentOn(both); len(got) != 2 {
		t.Errorf("PresentOn(both) = %v, want both clusters", got)
	}
	if got := m.Ratio(onlyB); got != 0.5 {
		t.Errorf("Ratio(only-b) = %v, want 0.5", got)
	}
}

// A rare (1/3) hand-applied resource gains fleet-rare +20: 80 → 100 (clamped).
func TestTier2_FleetRareRaisesSuspicion(t *testing.T) {
	cfg := config.Default().Analysis
	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{kubectlRes("debug-dump"), helmRes("web")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{helmRes("web")}},
		&inventory.Snapshot{Cluster: "c3", Resources: []inventory.Resource{helmRes("web")}},
	)

	findings, _ := analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)

	f := findByName(findings, analyze.ClassLikelyManual, "debug-dump")
	if f == nil {
		t.Fatalf("debug-dump not found in %+v", findings)
	}
	if f.Score.Base != 80 || f.Score.FleetModifier != 20 || f.Score.Total != 100 || !f.Score.FleetApplied {
		t.Errorf("Score = %+v, want base 80, modifier +20, total 100, fleetApplied", f.Score)
	}
	last := f.Score.Reasons[len(f.Score.Reasons)-1]
	if last.Code != "fleet-rare" || last.Points != 20 || last.Detail != "present on 1/3" {
		t.Errorf("last reason = %+v, want fleet-rare +20 'present on 1/3'", last)
	}
	if f.Presence == nil || f.Presence.FleetSize != 3 || len(f.Presence.PresentOn) != 1 || f.Presence.PresentOn[0] != "c1" {
		t.Errorf("Presence = %+v, want present on [c1] of 3", f.Presence)
	}
	if !strings.Contains(f.Message, "present on 1/3 clusters") {
		t.Errorf("Message = %q, want fleet presence appended", f.Message)
	}
}

// ADR-0002 sanity: fleet-rare promotes a base-35 finding (suppressed in Tier 1)
// to 55 → reported.
func TestTier2_FleetRarePromotesAboveThreshold(t *testing.T) {
	cfg := config.Default().Analysis
	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{unmanagedRes("crd-ish"), helmRes("web")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{helmRes("web")}},
		&inventory.Snapshot{Cluster: "c3", Resources: []inventory.Resource{helmRes("web")}},
	)

	findings, _ := analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)

	f := findByName(findings, analyze.ClassLikelyManual, "crd-ish")
	if f == nil {
		t.Fatalf("crd-ish not promoted: %+v", findings)
	}
	if f.Score.Base != 35 || f.Score.Total != 55 {
		t.Errorf("Score = %+v, want 35 + 20 = 55", f.Score)
	}
}

// ADR-0002 sanity: un-annotated-but-ubiquitous (base 56) is demoted 56−30=26 →
// suppressed; the same resource with kubectl evidence everywhere (base 80)
// stays visible at 50.
func TestTier2_FleetUbiquitousDemotes(t *testing.T) {
	cfg := config.Default().Analysis
	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{unmanagedSecretRes("legacy-cert"), kubectlRes("everywhere")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{unmanagedSecretRes("legacy-cert"), kubectlRes("everywhere")}},
		&inventory.Snapshot{Cluster: "c3", Resources: []inventory.Resource{unmanagedSecretRes("legacy-cert"), kubectlRes("everywhere")}},
	)

	findings, suppressed := analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)

	if f := findByName(findings, analyze.ClassLikelyManual, "legacy-cert"); f != nil {
		t.Errorf("legacy-cert (56-30=26) must be suppressed, got %+v", f)
	}
	if suppressed != 3 {
		t.Errorf("suppressed = %d, want 3 (legacy-cert on each of 3 clusters)", suppressed)
	}

	f := findByName(findings, analyze.ClassLikelyManual, "everywhere")
	if f == nil {
		t.Fatal("hand-applied-everywhere resource must stay visible (80-30=50)")
	}
	if f.Score.Total != 50 || f.Score.FleetModifier != -30 {
		t.Errorf("Score = %+v, want total 50, modifier -30", f.Score)
	}
	if len(f.Clusters) != 3 {
		t.Errorf("Clusters = %v, want merged across all 3", f.Clusters)
	}
	if len(f.CreationTimestamps) != 3 {
		t.Errorf("CreationTimestamps = %v, want one per cluster", f.CreationTimestamps)
	}
}

// ADR-0002 small-N semantics: at N == 2 the rare bucket is unreachable
// (r ∈ {0.5, 1.0}; 0.5 is the partial band) but ubiquitous still applies.
func TestTier2_SmallN(t *testing.T) {
	cfg := config.Default().Analysis
	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{kubectlRes("half"), kubectlRes("full")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{kubectlRes("full")}},
	)

	findings, _ := analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)

	half := findByName(findings, analyze.ClassLikelyManual, "half")
	if half == nil || half.Score.FleetModifier != 0 || !half.Score.FleetApplied || half.Score.Total != 80 {
		t.Errorf("half = %+v, want partial band: modifier 0, fleetApplied, total 80", half)
	}
	full := findByName(findings, analyze.ClassLikelyManual, "full")
	if full == nil || full.Score.FleetModifier != -30 || full.Score.Total != 50 {
		t.Errorf("full = %+v, want ubiquitous: modifier -30, total 50", full)
	}
}

// A cluster that failed to list a GVR is excluded from that identity's
// denominator and never reported as "missing" it.
func TestTier2_FailedGVRExcludedFromDenominator(t *testing.T) {
	cfg := config.Default().Analysis
	cfg.CommonThreshold = 0.6

	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{unmanagedSecretRes("shared"), helmRes("web")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{unmanagedSecretRes("shared"), helmRes("web")}},
		&inventory.Snapshot{Cluster: "c3", Resources: []inventory.Resource{helmRes("web")}},
	)
	// c3 couldn't list Secrets (e.g. RBAC forbids it).
	fleet.Failed["c3"] = map[config.KindRef]bool{{Kind: "Secret"}: true}

	findings, _ := analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)

	// N_k for the Secret is 2 (c1, c2), r == 1.0 → ubiquitous, not 2/3, and
	// no missing-from-cluster finding for c3.
	if f := findByName(findings, analyze.ClassMissingFromCluster, "shared"); f != nil {
		t.Errorf("must not report c3 missing a Secret it was forbidden to list: %+v", f)
	}
	f := findByName(findings, analyze.ClassLikelyManual, "shared")
	if f == nil {
		// 56 - 30 = 26: suppressed. That's fine — assert via presence on the
		// suppressed path instead by lowering the threshold.
		cfg.MinScore = 20
		findings, _ = analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)
		f = findByName(findings, analyze.ClassLikelyManual, "shared")
		if f == nil {
			t.Fatal("shared Secret not found even at threshold 20")
		}
	}
	if f.Presence.FleetSize != 2 || f.Presence.Ratio != 1.0 {
		t.Errorf("Presence = %+v, want fleetSize 2, ratio 1.0 (c3 excluded)", f.Presence)
	}
}

// Pipeline-owned resources in the common-with-gaps band produce a
// missing-from-cluster finding per absent cluster — the headline "NetworkPolicy
// that never landed on one cluster" case.
func TestTier2_MissingFromCluster(t *testing.T) {
	cfg := config.Default().Analysis
	cfg.CommonThreshold = 0.6

	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{helmRes("ingress")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{helmRes("ingress")}},
		&inventory.Snapshot{Cluster: "c3", Resources: []inventory.Resource{}},
	)
	// c3 serves apps/Deployment (fleetOf only auto-serves kinds it saw).
	fleet.Served["c3"] = map[config.KindRef]bool{{Group: "apps", Kind: "Deployment"}: true}

	findings, _ := analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)

	f := findByName(findings, analyze.ClassMissingFromCluster, "ingress")
	if f == nil {
		t.Fatalf("want missing-from-cluster finding for ingress, got %+v", findings)
	}
	if len(f.Clusters) != 1 || f.Clusters[0] != "c3" {
		t.Errorf("Clusters = %v, want [c3]", f.Clusters)
	}
	if f.Score.Total != 50 || len(f.Score.Reasons) != 0 {
		t.Errorf("Score = %+v, want fixed 50 with no reasons", f.Score)
	}
	if f.Ownership != nil {
		t.Errorf("Ownership = %+v, want nil (no verdict exists on the absent cluster)", f.Ownership)
	}
	wantMsg := "apps/Deployment default/ingress is present on 2/3 clusters but missing from c3"
	if f.Message != wantMsg {
		t.Errorf("Message = %q, want %q", f.Message, wantMsg)
	}
	if len(f.CreationTimestamps) != 2 {
		t.Errorf("CreationTimestamps = %v, want entries for c1, c2", f.CreationTimestamps)
	}

	// Config switch: off → not emitted.
	cfg.ReportMissingFromCluster = false
	findings, _ = analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)
	if f := findByName(findings, analyze.ClassMissingFromCluster, "ingress"); f != nil {
		t.Errorf("reportMissingFromCluster=false must suppress emission, got %+v", f)
	}
}

// r exactly at commonThreshold is common: 3 of 4 clusters at 0.75 reports the
// fourth.
func TestTier2_MissingFromCluster_AtThreshold(t *testing.T) {
	cfg := config.Default().Analysis
	cfg.CommonThreshold = 0.75

	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{helmRes("ingress")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{helmRes("ingress")}},
		&inventory.Snapshot{Cluster: "c3", Resources: []inventory.Resource{helmRes("ingress")}},
		&inventory.Snapshot{Cluster: "c4", Resources: []inventory.Resource{}},
	)
	fleet.Served["c4"] = map[config.KindRef]bool{{Group: "apps", Kind: "Deployment"}: true}

	findings, _ := analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)
	if f := findByName(findings, analyze.ClassMissingFromCluster, "ingress"); f == nil || f.Clusters[0] != "c4" {
		t.Fatalf("want ingress missing from c4 at r == commonThreshold, got %+v", findings)
	}
}

// The default commonThreshold (0.6) reports 2 of 3, the smallest fleet where
// "present on most clusters" means anything.
func TestTier2_MissingFromCluster_DefaultThreeClusters(t *testing.T) {
	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{helmRes("ingress")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{helmRes("ingress")}},
		&inventory.Snapshot{Cluster: "c3", Resources: []inventory.Resource{}},
	)
	fleet.Served["c3"] = map[config.KindRef]bool{{Group: "apps", Kind: "Deployment"}: true}

	findings, _ := analyze.Tier2Findings(snaps, fleet, config.Default().Analysis, fixedNow)
	if findByName(findings, analyze.ClassMissingFromCluster, "ingress") == nil {
		t.Fatalf("want a missing-from-cluster finding with defaults on 3 clusters, got %+v", findings)
	}
}

// The partial band (r < commonThreshold) and full presence emit no
// missing-from-cluster findings.
func TestTier2_NoMissingFromOutsideBand(t *testing.T) {
	cfg := config.Default().Analysis
	cfg.CommonThreshold = 0.75

	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{helmRes("partial"), helmRes("full")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{helmRes("partial"), helmRes("full")}},
		&inventory.Snapshot{Cluster: "c3", Resources: []inventory.Resource{helmRes("full")}},
	)

	findings, _ := analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)

	for _, f := range findings {
		if f.Class == analyze.ClassMissingFromCluster {
			t.Errorf("no missing-from findings expected (r=2/3 < 0.75 and r=1.0), got %+v", f)
		}
	}
}

// A {Group, Kind} served on only part of the fleet is not a finding by
// itself (api-missing-from-cluster was deferred): API types are compared only
// through the resources that exist.
func TestTier2_KindServedOnSubsetIsNotAFinding(t *testing.T) {
	cfg := config.Default().Analysis
	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{helmRes("web")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{helmRes("web")}},
		&inventory.Snapshot{Cluster: "c3", Resources: []inventory.Resource{helmRes("web")}},
	)
	crd := config.KindRef{Group: "acme.example.com", Kind: "Widget"}
	fleet.Served["c1"][crd] = true
	fleet.Served["c2"][crd] = true

	findings, _ := analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)
	for _, f := range findings {
		if f.Key.Kind == "Widget" {
			t.Errorf("want no finding for a type served on 2/3 clusters, got %+v", f)
		}
	}
}

// ADR-0002 sanity: helm-marked but rare = 18 + 20 = 38 — suppressed at the
// default threshold 40, reported when the threshold is lowered to 35
// ("ownership lowers suspicion, it doesn't exempt").
func TestTier2_HelmMarkedButRare(t *testing.T) {
	cfg := config.Default().Analysis
	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{helmRes("stray"), kubectlRes("anchor1")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{kubectlRes("anchor1")}},
		&inventory.Snapshot{Cluster: "c3", Resources: []inventory.Resource{kubectlRes("anchor1")}},
	)

	findings, _ := analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)
	if f := findByName(findings, analyze.ClassLikelyManual, "stray"); f != nil {
		t.Errorf("helm-rare @ 38 must be suppressed at threshold 40, got %+v", f)
	}

	cfg.MinScore = 35
	findings, _ = analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)
	f := findByName(findings, analyze.ClassLikelyManual, "stray")
	if f == nil {
		t.Fatal("helm-rare @ 38 must be reported at threshold 35")
	}
	if f.Score.Base != 18 || f.Score.FleetModifier != 20 || f.Score.Total != 38 {
		t.Errorf("Score = %+v, want 18 + 20 = 38", f.Score)
	}
	if f.Ownership.Owner != ownership.OwnerHelm {
		t.Errorf("Owner = %v, want helm", f.Ownership.Owner)
	}
}

// Identities whose effective denominator is < 2 fall back to plain Tier 1
// scoring (no modifier, FleetApplied false).
func TestTier2_SingleEligibleClusterFallsBackToTier1(t *testing.T) {
	cfg := config.Default().Analysis
	snaps, fleet := fleetOf(
		&inventory.Snapshot{Cluster: "c1", Resources: []inventory.Resource{kubectlRes("solo"), helmRes("web")}},
		&inventory.Snapshot{Cluster: "c2", Resources: []inventory.Resource{helmRes("web")}},
		&inventory.Snapshot{Cluster: "c3", Resources: []inventory.Resource{helmRes("web")}},
	)
	// Only c1 serves ConfigMaps at all (contrived, but exercises N_k < 2).
	delete(fleet.Served["c2"], config.KindRef{Kind: "ConfigMap"})
	delete(fleet.Served["c3"], config.KindRef{Kind: "ConfigMap"})

	findings, _ := analyze.Tier2Findings(snaps, fleet, cfg, fixedNow)

	f := findByName(findings, analyze.ClassLikelyManual, "solo")
	if f == nil {
		t.Fatalf("solo not found: %+v", findings)
	}
	if f.Score.FleetApplied || f.Score.FleetModifier != 0 || f.Score.Total != 80 {
		t.Errorf("Score = %+v, want plain Tier 1 (80, no fleet)", f.Score)
	}
	if f.Presence == nil || f.Presence.FleetSize != 1 {
		t.Errorf("Presence = %+v, want fleetSize 1", f.Presence)
	}
}
