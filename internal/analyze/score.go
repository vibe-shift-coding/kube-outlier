// Package analyze computes the ownership-signal score (Tier 1 in DESIGN.md)
// and turns scored resources into findings. The fleet presence modifiers
// (Tier 2, weights.go) are applied separately by Tier2Findings and
// BaselineFindings; the content comparison (Tier 3) lives in content.go.
package analyze

import (
	"fmt"
	"time"

	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
)

// ScoreReason explains one score contribution. Code values frozen in
// interfaces.md §4.
type ScoreReason struct {
	Code   string `json:"code"`
	Points int    `json:"points"` // signed
	Detail string `json:"detail,omitempty"`
}

// Score is a finding's 0–100 score and every contribution to it (ADR-0002).
type Score struct {
	Base          int           `json:"base"`          // ownership-signal sum before clamping
	FleetModifier int           `json:"fleetModifier"` // 0 when enrichment not applied
	Total         int           `json:"total"`         // clamp(Base+FleetModifier, 0, 100)
	FleetApplied  bool          `json:"fleetApplied"`
	Reasons       []ScoreReason `json:"reasons"`
}

// BaseScore applies the ADR-0002 base table exactly. Reasons contains only
// fired reasons, in table order. FleetModifier and FleetApplied are always
// the zero value here (Tier 2 applies them later); Total == clamp(Base, 0, 100).
func BaseScore(v ownership.OwnershipVerdict, r inventory.Resource, now time.Time) Score {
	var reasons []ScoreReason
	base := 0

	if hasSignal(v, "no-pipeline-markers") {
		reasons = append(reasons, ScoreReason{Code: "no-pipeline-markers", Points: noPipelineMarkersPoints})
		base += noPipelineMarkersPoints
	}
	if v.KubectlWritten {
		reasons = append(reasons, ScoreReason{Code: "kubectl-manager", Points: kubectlManagerPoints})
		base += kubectlManagerPoints
	}
	if hasSignal(v, "kubectl.last-applied") {
		reasons = append(reasons, ScoreReason{Code: "last-applied", Points: lastAppliedPoints})
		base += lastAppliedPoints
	}

	switch {
	case kindWeightHigh[r.Key.Kind]:
		reasons = append(reasons, ScoreReason{Code: "kind-weight", Points: kindWeightHighPoints, Detail: r.Key.Kind + ": high"})
		base += kindWeightHighPoints
	case kindWeightMedium[r.Key.Kind]:
		reasons = append(reasons, ScoreReason{Code: "kind-weight", Points: kindWeightMediumPoints, Detail: r.Key.Kind + ": medium"})
		base += kindWeightMediumPoints
	}

	days := ageInDays(now, r.CreationTimestamp.Time)
	if agePoints := ageBandPoints(days); agePoints > 0 {
		reasons = append(reasons, ScoreReason{Code: "age", Points: agePoints, Detail: fmt.Sprintf("%dd", days)})
		base += agePoints
	}

	return Score{
		Base:    base,
		Total:   clamp(base, 0, 100),
		Reasons: reasons,
	}
}

func hasSignal(v ownership.OwnershipVerdict, code string) bool {
	for _, s := range v.Signals {
		if s.Code == code {
			return true
		}
	}
	return false
}

// ageInDays is the whole-calendar-day difference between now and created,
// ignoring time-of-day (so a resource created at 09:30 and compared against
// "now" at 00:00 the same number of calendar days later still counts as a
// full day of age).
func ageInDays(now, created time.Time) int {
	days := int(truncateToDate(now).Sub(truncateToDate(created)).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

func truncateToDate(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func ageBandPoints(days int) int {
	switch {
	case days < 7:
		return ageUnder7dPoints
	case days < 30:
		return age7to30dPoints
	case days < 90:
		return age30to90dPoints
	default:
		return ageOver90dPoints
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
