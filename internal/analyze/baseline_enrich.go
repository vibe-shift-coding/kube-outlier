package analyze

import (
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
)

// OwnershipTallies classifies every resource in each snapshot and tallies
// primary owners per identity — the input for baseline Entry.OwnershipSummary.
// Nothing is skipped: baselines describe the whole fleet, controller-owned
// resources included (presence is presence).
func OwnershipTallies(snaps []*inventory.Snapshot, cfg config.AnalysisConfig) map[identity.ResourceKey]map[ownership.Owner]int {
	tallies := make(map[identity.ResourceKey]map[ownership.Owner]int)
	for _, snap := range snaps {
		for _, r := range snap.Resources {
			v := ownership.Classify(r, cfg)
			if tallies[r.Key] == nil {
				tallies[r.Key] = make(map[ownership.Owner]int)
			}
			tallies[r.Key][v.Owner]++
		}
	}
	return tallies
}

// BaselineEntryRef is one baseline entry distilled to what enrichment needs.
type BaselineEntryRef struct {
	ClusterCount int
	Ratio        float64
}

// BaselineRef is the fleet reference distilled from a loaded baseline
// artifact. Built in cmd: analyze may not import the baseline package
// (interfaces.md §1 DAG), the same handoff pattern as ServedKinds.
type BaselineRef struct {
	FleetSize int      // Provenance.ClusterCount
	Clusters  []string // Provenance.Clusters
	Entries   map[identity.ResourceKey]BaselineEntryRef
	Kinds     map[config.KindRef]bool // kinds the baseline covers (union over Entries)
}

// BaselineFindings is the --baseline counterpart of Tier2Findings for ONE
// target cluster: likely-manual findings with fleet modifiers taken from
// baseline ratios, plus missing-from-cluster findings for common baseline
// entries absent locally. served/failed describe the TARGET cluster's kinds (from its discovery
// output and GVRErrors), guarding against unproven absence exactly as the live
// fleet's effective denominators do.
func BaselineFindings(snap *inventory.Snapshot, ref BaselineRef, served map[config.KindRef]bool, failed map[config.KindRef]bool, cfg config.AnalysisConfig, now time.Time) ([]Finding, int) {
	var findings []Finding
	suppressed := 0

	present := make(map[identity.ResourceKey]bool, len(snap.Resources))
	for _, r := range snap.Resources {
		present[r.Key] = true
	}

	// Likely-manual: Tier 1 base per resource, modifier from the baseline
	// ratio when the resource's kind is covered by the artifact.
	for _, r := range snap.Resources {
		if isSystemObject(r) {
			continue
		}
		if len(r.OwnerReferences) > 0 { // controller-owned: the parent is the interesting object
			continue
		}
		verdict := ownership.Classify(r, cfg)
		score := BaseScore(verdict, r, now)

		kr := config.KindRef{Group: r.Key.Group, Kind: r.Key.Kind}
		var presence *Presence
		reasonCode := ""
		if ref.Kinds[kr] {
			entry := ref.Entries[r.Key] // zero value when unseen: count 0, ratio 0
			presence = &Presence{
				PresentOn:    []string{},
				MissingFrom:  []string{},
				PresentCount: entry.ClusterCount,
				FleetSize:    ref.FleetSize,
				Ratio:        entry.Ratio,
			}
			if ref.FleetSize >= 2 {
				modifier := 0
				switch {
				case entry.Ratio <= cfg.RareThreshold:
					modifier, reasonCode = fleetRarePoints, "fleet-rare"
				case entry.Ratio == 1.0:
					modifier, reasonCode = fleetCommonPoints, "fleet-common"
				}
				score.FleetModifier = modifier
				score.FleetApplied = true
				score.Total = clamp(score.Base+modifier, 0, 100)
				if reasonCode != "" {
					score.Reasons = append(score.Reasons, ScoreReason{
						Code:   reasonCode,
						Points: modifier,
						Detail: fmt.Sprintf("present on %d/%d baseline clusters", entry.ClusterCount, ref.FleetSize),
					})
				}
			}
		}

		if score.Total < cfg.MinScore {
			suppressed++
			continue
		}

		msg := findingMessage(r.Key, verdict)
		if reasonCode != "" {
			msg = fmt.Sprintf("%s, present on %d/%d baseline clusters",
				msg, ref.Entries[r.Key].ClusterCount, ref.FleetSize)
		}
		findings = append(findings, Finding{
			Class:              ClassLikelyManual,
			Key:                r.Key,
			Clusters:           []string{snap.Cluster},
			Ownership:          &verdict,
			Score:              score,
			Presence:           presence,
			CreationTimestamps: map[string]metav1.Time{snap.Cluster: r.CreationTimestamp},
			AgeDays:            intPtr(ageInDays(now, r.CreationTimestamp.Time)),
			Message:            msg,
		})
	}

	// missing-from-cluster: baseline entries common in the reference fleet
	// but absent from the target. Unlike the live fleet the band is
	// [commonThreshold, 1.0] — the target is not part of the reference
	// fleet, so full baseline presence plus local absence still qualifies.
	if cfg.ReportMissingFromCluster {
		keys := make([]identity.ResourceKey, 0, len(ref.Entries))
		for k := range ref.Entries {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })

		for _, key := range keys {
			entry := ref.Entries[key]
			if entry.Ratio < cfg.CommonThreshold || present[key] || isSystemIgnored(key) {
				continue
			}
			kr := config.KindRef{Group: key.Group, Kind: key.Kind}
			if !served[kr] || failed[kr] {
				continue // absence unproven: target couldn't (cleanly) list this kind
			}
			if fixedClassScore < cfg.MinScore {
				suppressed++
				continue
			}
			findings = append(findings, Finding{
				Class:    ClassMissingFromCluster,
				Key:      key,
				Clusters: []string{snap.Cluster},
				Score:    fixedScore(),
				Presence: &Presence{
					PresentOn:    []string{},
					MissingFrom:  []string{},
					PresentCount: entry.ClusterCount,
					FleetSize:    ref.FleetSize,
					Ratio:        entry.Ratio,
				},
				CreationTimestamps: map[string]metav1.Time{},
				Message: fmt.Sprintf("%s is present on %d/%d baseline clusters but missing from %s",
					keyDisplay(key), entry.ClusterCount, ref.FleetSize, snap.Cluster),
			})
		}
	}

	SortFindings(findings)
	return findings, suppressed
}
