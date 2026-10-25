package report

import (
	"fmt"
	"io"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
)

// Explain is the assembled evidence for one identity (--why). Built
// in cmd, rendered here. Text-only — no JSON shape exists yet.
type Explain struct {
	Key    identity.ResourceKey
	Status string // e.g. "reported (…)", "below the minimum score (…)", "skipped: controller-owned (…)"
	// Notes are further status lines, one per other finding about this
	// identity ("reported as missing-from-cluster: …") or why it has none.
	Notes []string

	// Occurrences: one per cluster where the identity was found, sorted by
	// cluster name. Empty means not found anywhere in scope.
	Occurrences []ExplainOccurrence

	Score    *analyze.Score    // nil when never scored (e.g. controller-owned)
	Presence *analyze.Presence // nil without fleet/baseline context
	Source   string            // fleet presence source: "none" | "live" | "baseline"

	// Content holds the content-differs findings for this identity, rendered
	// with their field changes. Empty without --compare-content.
	Content []analyze.Finding
}

// ExplainOccurrence is the per-cluster evidence.
type ExplainOccurrence struct {
	Cluster           string
	Ownership         ownership.OwnershipVerdict
	CreationTimestamp metav1.Time
	AgeDays           int
	OwnerReferences   []string // "Kind name" summaries; empty when none
}

// RenderExplain writes the human detail view. Never prints Secret values —
// everything here is metadata-derived by construction.
func RenderExplain(w io.Writer, e Explain) error {
	var b strings.Builder

	fmt.Fprintf(&b, "%s\n\n", e.Key.String())
	fmt.Fprintf(&b, "status: %s\n", e.Status)
	for _, n := range e.Notes {
		fmt.Fprintf(&b, "        %s\n", n)
	}

	if e.Presence != nil {
		fmt.Fprintf(&b, "presence: %d/%d clusters (ratio %.2f, source %s)\n", e.Presence.PresentCount, e.Presence.FleetSize, e.Presence.Ratio, e.Source)
		if len(e.Presence.PresentOn) > 0 {
			fmt.Fprintf(&b, "  present on:   %s\n", strings.Join(e.Presence.PresentOn, ", "))
		}
		if len(e.Presence.MissingFrom) > 0 {
			fmt.Fprintf(&b, "  missing from: %s\n", strings.Join(e.Presence.MissingFrom, ", "))
		}
	}

	for _, occ := range e.Occurrences {
		fmt.Fprintf(&b, "\ncluster %s:\n", occ.Cluster)
		fmt.Fprintf(&b, "  created: %s (%s ago)\n", occ.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"), humanizeAge(occ.AgeDays))
		fmt.Fprintf(&b, "  owner: %s\n", OwnerCell(&occ.Ownership))
		if len(occ.Ownership.Managers) > 0 {
			fmt.Fprintf(&b, "  managers: %s\n", strings.Join(occ.Ownership.Managers, ", "))
		}
		if len(occ.OwnerReferences) > 0 {
			fmt.Fprintf(&b, "  ownerReferences: %s\n", strings.Join(occ.OwnerReferences, ", "))
		}
		if len(occ.Ownership.Signals) > 0 {
			b.WriteString("  signals:\n")
			for _, s := range occ.Ownership.Signals {
				fmt.Fprintf(&b, "    %-24s %s\n", s.Code, s.Detail)
			}
		}
	}

	if e.Score != nil {
		b.WriteString("\nscore:\n")
		for _, r := range e.Score.Reasons {
			detail := ""
			if r.Detail != "" {
				detail = "  (" + r.Detail + ")"
			}
			fmt.Fprintf(&b, "  %-20s %+d%s\n", r.Code, r.Points, detail)
		}
		total := fmt.Sprintf("  total: %d", e.Score.Total)
		if e.Score.FleetApplied {
			total += fmt.Sprintf(" (base %d, fleet %+d)", e.Score.Base, e.Score.FleetModifier)
		}
		b.WriteString(total + "\n")
	}

	if len(e.Content) > 0 {
		b.WriteString("\ncontent differences:\n")
		for _, f := range e.Content {
			writeContentFinding(&b, f)
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}
