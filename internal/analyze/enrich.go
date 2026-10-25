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

// ServedKinds is, per cluster name, a set of {Group, Kind}. Built in cmd from
// discovery.ListScannable output (analyze may not import discovery —
// interfaces.md §1 DAG).
type ServedKinds map[string]map[config.KindRef]bool

// FleetInputs bundles the fleet-wide context Tier2Findings needs beyond the
// snapshots themselves. A cluster counts toward an identity's presence
// denominator only if it serves the kind (Served) and listed it cleanly (not
// in Failed, the kinds whose GVR recorded a GVRError there), so a cluster that
// couldn't list a type is never treated as "the resource is absent there".
type FleetInputs struct {
	Matrix *identity.PresenceMatrix
	Served ServedKinds
	Failed ServedKinds
}

// BuildPresence builds the presence matrix from every snapshot's resources.
// Clusters = the snapshot cluster names, sorted. Call only when
// len(snaps) >= 2 (single-cluster scans never build a matrix).
func BuildPresence(snaps []*inventory.Snapshot) *identity.PresenceMatrix {
	names := make([]string, 0, len(snaps))
	for _, s := range snaps {
		names = append(names, s.Cluster)
	}
	sort.Strings(names)

	m := identity.NewPresenceMatrix(names)
	for _, s := range snaps {
		for _, r := range s.Resources {
			m.Add(r.Key, s.Cluster)
		}
	}
	return m
}

// fleetContext is the per-identity enrichment computation: the effective
// denominator (eligible clusters), presence ratio, and the fleet modifier it
// implies (ADR-0002).
type fleetContext struct {
	presentOn   []string // sorted
	missingFrom []string // eligible − presentOn, sorted
	fleetSize   int      // N_k: eligible clusters for this identity's {Group,Kind}
	ratio       float64
	modifier    int    // fleetRarePoints, fleetCommonPoints, or 0
	reasonCode  string // "fleet-rare" | "fleet-common" | ""
	evaluated   bool   // false when N_k < 2: enrichment not applicable
}

// fleetContextFor computes the enrichment context for one identity. Eligible
// clusters are those whose discovery served the identity's {Group, Kind} and
// which recorded no GVRError for it; clusters the identity was actually seen
// on are always eligible (presence is proof of listability).
func fleetContextFor(k identity.ResourceKey, fleet FleetInputs, cfg config.AnalysisConfig) fleetContext {
	kr := config.KindRef{Group: k.Group, Kind: k.Kind}

	presentSet := make(map[string]bool)
	for _, c := range fleet.Matrix.PresentOn(k) {
		presentSet[c] = true
	}

	var eligible []string
	for _, c := range fleet.Matrix.Clusters {
		if presentSet[c] || (fleet.Served[c][kr] && !fleet.Failed[c][kr]) {
			eligible = append(eligible, c)
		}
	}

	fc := fleetContext{fleetSize: len(eligible)}
	for _, c := range eligible {
		if presentSet[c] {
			fc.presentOn = append(fc.presentOn, c)
		} else {
			fc.missingFrom = append(fc.missingFrom, c)
		}
	}
	if fc.fleetSize > 0 {
		fc.ratio = float64(len(fc.presentOn)) / float64(fc.fleetSize)
	}
	if fc.fleetSize < 2 {
		return fc
	}

	fc.evaluated = true
	switch {
	case fc.ratio <= cfg.RareThreshold:
		fc.modifier, fc.reasonCode = fleetRarePoints, "fleet-rare"
	case fc.ratio == 1.0:
		fc.modifier, fc.reasonCode = fleetCommonPoints, "fleet-common"
	}
	return fc
}

// enrichScore applies fc's fleet modifier to an ownership-signal base score,
// re-clamping Total and appending the fired reason (ADR-0002: the minScore
// filter compares against this post-modifier Total).
func enrichScore(base Score, fc fleetContext) Score {
	if !fc.evaluated {
		return base
	}
	s := base
	s.FleetModifier = fc.modifier
	s.FleetApplied = true
	s.Total = clamp(s.Base+s.FleetModifier, 0, 100)
	if fc.reasonCode != "" {
		s.Reasons = append(append([]ScoreReason(nil), base.Reasons...), ScoreReason{
			Code:   fc.reasonCode,
			Points: fc.modifier,
			Detail: fmt.Sprintf("present on %d/%d", len(fc.presentOn), fc.fleetSize),
		})
	}
	return s
}

// Tier2Findings replaces Tier1Findings when a scan has two or more clusters
// with snapshots. It returns findings of both inventory classes —
// likely-manual (fleet-modified and merged per identity across clusters) and
// missing-from-cluster — plus the
// suppressed-below-minScore count, sorted in the global report order.
func Tier2Findings(snaps []*inventory.Snapshot, fleet FleetInputs, cfg config.AnalysisConfig, now time.Time) ([]Finding, int) {
	var findings []Finding
	suppressed := 0
	systemKeys := systemObjectKeys(snaps)

	// Per-identity creation timestamps across all clusters (all ownership
	// types: missing-from-cluster findings describe pipeline-managed
	// resources too).
	created := make(map[identity.ResourceKey]map[string]metav1.Time)
	for _, snap := range snaps {
		for _, r := range snap.Resources {
			if created[r.Key] == nil {
				created[r.Key] = make(map[string]metav1.Time)
			}
			created[r.Key][snap.Cluster] = r.CreationTimestamp
		}
	}

	// Likely-manual: classify and base-score per cluster exactly as Tier 1,
	// then apply the identity's fleet modifier, minScore-filter on the
	// post-modifier Total, and merge surviving clusters into one finding.
	type clusterScore struct {
		cluster string
		verdict ownership.OwnershipVerdict
		score   Score
	}
	scored := make(map[identity.ResourceKey][]clusterScore)
	var scoredKeys []identity.ResourceKey
	for _, snap := range snaps {
		for _, r := range snap.Resources {
			if systemKeys[r.Key] {
				continue
			}
			if len(r.OwnerReferences) > 0 { // controller-owned: the parent is the interesting object
				continue
			}
			verdict := ownership.Classify(r, cfg)
			if _, seen := scored[r.Key]; !seen {
				scoredKeys = append(scoredKeys, r.Key)
			}
			scored[r.Key] = append(scored[r.Key], clusterScore{
				cluster: snap.Cluster,
				verdict: verdict,
				score:   BaseScore(verdict, r, now),
			})
		}
	}

	fcByKey := make(map[identity.ResourceKey]fleetContext, len(scoredKeys))
	for _, key := range scoredKeys {
		fc := fleetContextFor(key, fleet, cfg)
		fcByKey[key] = fc

		var kept []clusterScore
		for _, cs := range scored[key] {
			cs.score = enrichScore(cs.score, fc)
			if cs.score.Total < cfg.MinScore {
				suppressed++
				continue
			}
			kept = append(kept, cs)
		}
		if len(kept) == 0 {
			continue
		}

		// Highest Total wins; ties break on cluster name so the merge is
		// deterministic.
		sort.Slice(kept, func(i, j int) bool {
			if kept[i].score.Total != kept[j].score.Total {
				return kept[i].score.Total > kept[j].score.Total
			}
			return kept[i].cluster < kept[j].cluster
		})
		best := kept[0]

		clusters := make([]string, 0, len(kept))
		timestamps := make(map[string]metav1.Time, len(kept))
		for _, cs := range kept {
			clusters = append(clusters, cs.cluster)
			timestamps[cs.cluster] = created[key][cs.cluster]
		}
		sort.Strings(clusters)

		verdict := best.verdict
		findings = append(findings, Finding{
			Class:              ClassLikelyManual,
			Key:                key,
			Clusters:           clusters,
			Ownership:          &verdict,
			Score:              best.score,
			Presence:           fc.presence(),
			CreationTimestamps: timestamps,
			AgeDays:            oldestAgeDays(now, timestamps),
			Message:            enrichedMessage(key, verdict, fc),
		})
	}

	// missing-from-cluster: identities in the common band (commonThreshold
	// <= r < 1), one finding per absent eligible cluster, regardless of
	// ownership.
	if cfg.ReportMissingFromCluster {
		for _, key := range sortedKeys(fleet.Matrix) {
			if systemKeys[key] {
				continue
			}
			fc, ok := fcByKey[key]
			if !ok {
				fc = fleetContextFor(key, fleet, cfg)
			}
			if !fc.evaluated || fc.ratio < cfg.CommonThreshold || fc.ratio >= 1.0 {
				continue
			}
			for _, absent := range fc.missingFrom {
				if fixedClassScore < cfg.MinScore {
					suppressed++
					continue
				}
				findings = append(findings, Finding{
					Class:              ClassMissingFromCluster,
					Key:                key,
					Clusters:           []string{absent},
					Score:              fixedScore(),
					Presence:           fc.presence(),
					CreationTimestamps: copyTimestamps(created[key]),
					AgeDays:            oldestAgeDays(now, created[key]),
					Message: fmt.Sprintf("%s is present on %d/%d clusters but missing from %s",
						keyDisplay(key), len(fc.presentOn), fc.fleetSize, absent),
				})
			}
		}
	}

	SortFindings(findings)
	return findings, suppressed
}

// presence renders the fleetContext as the report's Presence block.
func (fc fleetContext) presence() *Presence {
	return &Presence{
		PresentOn:    emptyIfNilStrings(fc.presentOn),
		MissingFrom:  emptyIfNilStrings(fc.missingFrom),
		PresentCount: len(fc.presentOn),
		FleetSize:    fc.fleetSize,
		Ratio:        fc.ratio,
	}
}

// fixedScore is the Score for the fixed-score fleet classes (ADR-0002):
// constant Total, no reasons — there is no per-resource evidence to weigh.
func fixedScore() Score {
	return Score{
		Base:         fixedClassScore,
		Total:        fixedClassScore,
		FleetApplied: true,
		Reasons:      []ScoreReason{},
	}
}

// enrichedMessage appends fleet presence to the Tier 1 message when a modifier
// fired (neutral wording rule stands: "unusual", never "wrong").
func enrichedMessage(k identity.ResourceKey, v ownership.OwnershipVerdict, fc fleetContext) string {
	msg := findingMessage(k, v)
	if fc.reasonCode != "" {
		msg = fmt.Sprintf("%s, present on %d/%d clusters", msg, len(fc.presentOn), fc.fleetSize)
	}
	return msg
}

// keyDisplay renders the identity string ("apps/Deployment ns/name"), the
// form the table, --why and baseline exceptions use, so a message names the
// resource the same way everywhere.
func keyDisplay(k identity.ResourceKey) string {
	return k.String()
}

// sortedKeys returns the matrix's identities in Key.String() order for
// deterministic output.
func sortedKeys(m *identity.PresenceMatrix) []identity.ResourceKey {
	keys := make([]identity.ResourceKey, 0, len(m.ByKey))
	for k := range m.ByKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

// oldestAgeDays is now − the oldest creation timestamp, whole days; nil when
// there are no timestamps (the age is unknown, not zero).
func oldestAgeDays(now time.Time, timestamps map[string]metav1.Time) *int {
	if len(timestamps) == 0 {
		return nil
	}
	oldest := 0
	for _, ts := range timestamps {
		if d := ageInDays(now, ts.Time); d > oldest {
			oldest = d
		}
	}
	return &oldest
}

func copyTimestamps(in map[string]metav1.Time) map[string]metav1.Time {
	out := make(map[string]metav1.Time, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// emptyIfNilStrings keeps Presence's sorted lists marshalling as JSON arrays
// ("[]"), never null (interfaces.md §3 array rule).
func emptyIfNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
