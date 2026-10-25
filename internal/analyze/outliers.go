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

// FindingClass names what kind of unusual thing a finding describes. The
// values are a frozen enum (interfaces.md §2.6).
type FindingClass string

// The finding classes. The first two are "inventory" findings for
// --fail-on; content-differs is the "content" group.
const (
	ClassLikelyManual       FindingClass = "likely-manual"        // one resource, probably created by hand
	ClassMissingFromCluster FindingClass = "missing-from-cluster" // common resource absent from one cluster
	ClassContentDiffers     FindingClass = "content-differs"      // copies of a shared resource differ
)

// Presence is nil unless the fleet presence signal (live or baseline) applied.
// Never fabricate it. PresentCount is always exact; the name lists are empty
// in baseline mode, because a baseline stores counts, not cluster names.
type Presence struct {
	PresentOn    []string `json:"presentOn"`    // cluster names, sorted
	MissingFrom  []string `json:"missingFrom"`  // cluster names, sorted
	PresentCount int      `json:"presentCount"` // clusters that have it (== len(PresentOn) in live mode)
	FleetSize    int      `json:"fleetSize"`    // N (reachable) or baseline cluster count
	Ratio        float64  `json:"ratio"`
}

// Finding is one row of the report (interfaces.md §2.6, JSON schema §3).
// Ownership is nil for the classes that describe the fleet rather than one
// object (missing-from-cluster, content-differs). AgeDays is nil when no
// creation time is known (baseline-mode missing-from-cluster).
type Finding struct {
	Class              FindingClass                `json:"class"`
	Key                identity.ResourceKey        `json:"resource"`
	Clusters           []string                    `json:"clusters"` // clusters this finding is about, sorted; for missing-from-cluster, where it is missing
	Ownership          *ownership.OwnershipVerdict `json:"ownership,omitempty"`
	Score              Score                       `json:"score"`
	Presence           *Presence                   `json:"presence,omitempty"`
	Content            *ContentDetail              `json:"content,omitempty"`  // only on Class==content-differs
	CreationTimestamps map[string]metav1.Time      `json:"creationTimestamps"` // per cluster name
	AgeDays            *int                        `json:"ageDays,omitempty"`  // now − oldest CreationTimestamp, whole days
	Message            string                      `json:"message"`            // one-line neutral summary ("unusual", never "wrong")
}

// Tier1Findings classifies and scores every resource in snap, skipping
// controller-owned resources (non-empty ownerReferences) and suppressing
// anything scoring below cfg.MinScore. It returns the surviving
// findings, sorted by Score.Total desc then Key.String() asc, plus the count
// below the minimum score.
func Tier1Findings(snap *inventory.Snapshot, cfg config.AnalysisConfig, now time.Time) ([]Finding, int) {
	var findings []Finding
	suppressed := 0

	for _, r := range snap.Resources {
		if isSystemObject(r) {
			continue
		}
		if len(r.OwnerReferences) > 0 { // controller-owned: the parent is the interesting object
			continue
		}

		verdict := ownership.Classify(r, cfg)
		score := BaseScore(verdict, r, now)
		if score.Total < cfg.MinScore {
			suppressed++
			continue
		}

		findings = append(findings, Finding{
			Class:              ClassLikelyManual,
			Key:                r.Key,
			Clusters:           []string{snap.Cluster},
			Ownership:          &verdict,
			Score:              score,
			CreationTimestamps: map[string]metav1.Time{snap.Cluster: r.CreationTimestamp},
			AgeDays:            intPtr(ageInDays(now, r.CreationTimestamp.Time)),
			Message:            findingMessage(r.Key, verdict),
		})
	}

	SortFindings(findings)
	return findings, suppressed
}

// SortFindings sorts in place by Score.Total desc, then Key.String() asc —
// the global order the JSON report and table both require, including when
// findings from several clusters are merged together.
func SortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Score.Total != findings[j].Score.Total {
			return findings[i].Score.Total > findings[j].Score.Total
		}
		return findings[i].Key.String() < findings[j].Key.String()
	})
}

// findingMessage is the one-line neutral summary for a likely-manual
// finding. It states what was observed, never a verdict: an owner from a
// pipeline marker plus a kubectl write is described as exactly that.
func findingMessage(k identity.ResourceKey, v ownership.OwnershipVerdict) string {
	subject := keyDisplay(k)
	switch {
	case v.Owner == ownership.OwnerKubectl:
		return subject + " was written by a kubectl client and has no pipeline markers"
	case v.KubectlWritten:
		return fmt.Sprintf("%s carries %s markers but was also written by a kubectl client", subject, ownerDisplay(v.Owner))
	case v.Owner == ownership.OwnerUnknown:
		return subject + " has no recognized pipeline ownership"
	default:
		return fmt.Sprintf("%s carries %s markers", subject, ownerDisplay(v.Owner))
	}
}

// ownerDisplay is the product name for an owner value, for messages.
func ownerDisplay(o ownership.Owner) string {
	switch o {
	case ownership.OwnerHelm:
		return "Helm"
	case ownership.OwnerArgoCD:
		return "Argo CD"
	case ownership.OwnerFlux:
		return "Flux"
	case ownership.OwnerController:
		return "controller (ownerReferences)"
	default:
		return string(o)
	}
}

func intPtr(v int) *int { return &v }
