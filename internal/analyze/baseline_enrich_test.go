package analyze_test

import (
	"strings"
	"testing"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
)

// refOver builds a BaselineRef covering the given entries' kinds, fleet of 3.
func refOver(entries map[identity.ResourceKey]analyze.BaselineEntryRef) analyze.BaselineRef {
	kinds := map[config.KindRef]bool{}
	for k := range entries {
		kinds[config.KindRef{Group: k.Group, Kind: k.Kind}] = true
	}
	return analyze.BaselineRef{FleetSize: 3, Entries: entries, Kinds: kinds}
}

func servedAll() map[config.KindRef]bool {
	return map[config.KindRef]bool{
		{Kind: "ConfigMap"}: true, {Kind: "Secret"}: true,
		{Group: "apps", Kind: "Deployment"}:                 true,
		{Group: "networking.k8s.io", Kind: "NetworkPolicy"}: true,
	}
}

func noFailed() map[config.KindRef]bool { return map[config.KindRef]bool{} }

func TestOwnershipTallies(t *testing.T) {
	cfg := config.Default().Analysis
	snaps := []*inventory.Snapshot{
		{Cluster: "c1", Resources: []inventory.Resource{kubectlRes("x"), helmRes("web")}},
		{Cluster: "c2", Resources: []inventory.Resource{unmanagedRes("x"), helmRes("web")}},
	}

	tallies := analyze.OwnershipTallies(snaps, cfg)

	xKey := identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "x"}
	webKey := identity.ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"}
	if got := tallies[xKey]; got[ownership.OwnerKubectl] != 1 || got[ownership.OwnerUnknown] != 1 {
		t.Errorf("tallies[x] = %v, want kubectl:1 unknown:1", got)
	}
	if got := tallies[webKey]; got[ownership.OwnerHelm] != 2 {
		t.Errorf("tallies[web] = %v, want helm:2", got)
	}
}

// A resource with no baseline entry gets r=0 -> fleet-rare; one with ratio
// 1.0 gets demoted; the reason detail counts baseline clusters.
func TestBaselineFindings_Modifiers(t *testing.T) {
	cfg := config.Default().Analysis
	snap := &inventory.Snapshot{Cluster: "d", Resources: []inventory.Resource{
		kubectlRes("debug-dump"),          // no baseline entry -> r=0 -> 80+20=100
		unmanagedSecretRes("legacy-cert"), // baseline ratio 1.0 -> 56-30=26 suppressed
	}}
	ref := refOver(map[identity.ResourceKey]analyze.BaselineEntryRef{
		{Kind: "Secret", Namespace: "default", Name: "legacy-cert"}: {ClusterCount: 3, Ratio: 1.0},
		// ConfigMap kind must be covered for r=0 to mean "rare", so add any
		// ConfigMap entry to the artifact.
		{Kind: "ConfigMap", Namespace: "default", Name: "cluster-info"}: {ClusterCount: 3, Ratio: 1.0},
	})
	// Suppress the cluster-info missing-from noise for this test.
	cfg.ReportMissingFromCluster = false

	findings, suppressed := analyze.BaselineFindings(snap, ref, servedAll(), noFailed(), cfg, fixedNow)

	f := findByName(findings, analyze.ClassLikelyManual, "debug-dump")
	if f == nil {
		t.Fatalf("debug-dump not found: %+v", findings)
	}
	if f.Score.Total != 100 || f.Score.FleetModifier != 20 || !f.Score.FleetApplied {
		t.Errorf("Score = %+v, want 80+20=100 fleet-rare", f.Score)
	}
	last := f.Score.Reasons[len(f.Score.Reasons)-1]
	if last.Code != "fleet-rare" || last.Detail != "present on 0/3 baseline clusters" {
		t.Errorf("reason = %+v, want fleet-rare 'present on 0/3 baseline clusters'", last)
	}
	if f.Presence == nil || f.Presence.FleetSize != 3 || f.Presence.Ratio != 0 {
		t.Errorf("Presence = %+v, want fleetSize 3 ratio 0", f.Presence)
	}
	if !strings.Contains(f.Message, "present on 0/3 baseline clusters") {
		t.Errorf("Message = %q", f.Message)
	}

	if f := findByName(findings, analyze.ClassLikelyManual, "legacy-cert"); f != nil {
		t.Errorf("legacy-cert (56-30=26) must be suppressed, got %+v", f)
	}
	if suppressed != 1 {
		t.Errorf("suppressed = %d, want 1", suppressed)
	}
}

// A kind the baseline does not cover gets plain Tier 1 treatment: no
// modifier, no presence, FleetApplied false — no data is not rarity.
func TestBaselineFindings_UncoveredKind(t *testing.T) {
	cfg := config.Default().Analysis
	snap := &inventory.Snapshot{Cluster: "d", Resources: []inventory.Resource{kubectlRes("debug-dump")}}
	ref := refOver(map[identity.ResourceKey]analyze.BaselineEntryRef{
		{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"}: {ClusterCount: 3, Ratio: 1.0},
	})

	findings, _ := analyze.BaselineFindings(snap, ref, servedAll(), noFailed(), cfg, fixedNow)

	f := findByName(findings, analyze.ClassLikelyManual, "debug-dump")
	if f == nil {
		t.Fatal("debug-dump must still be a plain Tier 1 finding")
	}
	if f.Score.Total != 80 || f.Score.FleetApplied || f.Presence != nil {
		t.Errorf("want plain Tier 1 (80, no fleet, no presence), got score %+v presence %+v", f.Score, f.Presence)
	}
}

// A single-cluster baseline is valid but carries no rarity evidence.
func TestBaselineFindings_FleetSizeOne(t *testing.T) {
	cfg := config.Default().Analysis
	snap := &inventory.Snapshot{Cluster: "d", Resources: []inventory.Resource{kubectlRes("debug-dump")}}
	ref := analyze.BaselineRef{
		FleetSize: 1,
		Entries:   map[identity.ResourceKey]analyze.BaselineEntryRef{},
		Kinds:     map[config.KindRef]bool{{Kind: "ConfigMap"}: true},
	}

	findings, _ := analyze.BaselineFindings(snap, ref, servedAll(), noFailed(), cfg, fixedNow)

	f := findByName(findings, analyze.ClassLikelyManual, "debug-dump")
	if f == nil || f.Score.FleetApplied || f.Score.FleetModifier != 0 || f.Score.Total != 80 {
		t.Errorf("f = %+v, want no modifiers at FleetSize 1", f)
	}
	if f.Presence == nil || f.Presence.FleetSize != 1 {
		t.Errorf("Presence = %+v, want fleetSize 1 populated", f.Presence)
	}
}

// Baseline entries common in the reference fleet but absent locally become
// missing-from-cluster findings — including ratio 1.0, unlike the live fleet.
func TestBaselineFindings_MissingFromCluster(t *testing.T) {
	cfg := config.Default().Analysis
	cfg.CommonThreshold = 0.75
	snap := &inventory.Snapshot{Cluster: "d", Resources: []inventory.Resource{}}
	npKey := identity.ResourceKey{Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: "shop", Name: "default-deny"}
	partialKey := identity.ResourceKey{Kind: "ConfigMap", Namespace: "shop", Name: "regional"}
	ref := refOver(map[identity.ResourceKey]analyze.BaselineEntryRef{
		npKey:      {ClusterCount: 3, Ratio: 1.0},       // absent locally -> finding
		partialKey: {ClusterCount: 2, Ratio: 2.0 / 3.0}, // below commonThreshold -> no finding
	})

	findings, _ := analyze.BaselineFindings(snap, ref, servedAll(), noFailed(), cfg, fixedNow)

	f := findByName(findings, analyze.ClassMissingFromCluster, "default-deny")
	if f == nil {
		t.Fatalf("want missing-from finding for default-deny, got %+v", findings)
	}
	if f.Clusters[0] != "d" || f.Score.Total != 50 {
		t.Errorf("f = %+v, want cluster d @ 50", f)
	}
	want := "networking.k8s.io/NetworkPolicy shop/default-deny is present on 3/3 baseline clusters but missing from d"
	if f.Message != want {
		t.Errorf("Message = %q, want %q", f.Message, want)
	}
	if findByName(findings, analyze.ClassMissingFromCluster, "regional") != nil {
		t.Error("ratio 2/3 < commonThreshold 0.75 must not produce a finding")
	}

	// Kind failed on the target -> absence unproven -> no finding.
	failed := map[config.KindRef]bool{{Group: "networking.k8s.io", Kind: "NetworkPolicy"}: true}
	findings, _ = analyze.BaselineFindings(snap, ref, servedAll(), failed, cfg, fixedNow)
	if findByName(findings, analyze.ClassMissingFromCluster, "default-deny") != nil {
		t.Error("failed kind must not produce a missing-from finding")
	}

	// Kind not served on the target -> same.
	served := servedAll()
	delete(served, config.KindRef{Group: "networking.k8s.io", Kind: "NetworkPolicy"})
	findings, _ = analyze.BaselineFindings(snap, ref, served, noFailed(), cfg, fixedNow)
	if findByName(findings, analyze.ClassMissingFromCluster, "default-deny") != nil {
		t.Error("unserved kind must not produce a missing-from finding")
	}

	// Config switch off.
	cfg.ReportMissingFromCluster = false
	findings, _ = analyze.BaselineFindings(snap, ref, servedAll(), noFailed(), cfg, fixedNow)
	if findByName(findings, analyze.ClassMissingFromCluster, "default-deny") != nil {
		t.Error("reportMissingFromCluster=false must suppress emission")
	}
}
