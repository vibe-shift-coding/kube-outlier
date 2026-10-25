package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
)

// contentDetailString renders the per-identity content-differences block plus the
// fleet-wide summary of the fields that differ most (docs/DESIGN.md §8), for
// printing after the findings table. Empty when there are no content-differs
// findings. Secret values never appear — they are replaced by keyed digests
// before a report is built (ADR-0006).
func contentDetailString(rep *Report) string {
	var differs []analyze.Finding
	for _, f := range rep.Findings {
		if f.Class == analyze.ClassContentDiffers && f.Content != nil {
			differs = append(differs, f)
		}
	}
	if len(differs) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\nContent differences:\n")
	for _, f := range differs {
		writeContentFinding(&b, f)
	}

	b.WriteString(mostDifferingFieldsString(differs))
	return b.String()
}

// writeContentFinding writes one content-differs finding: its reference
// version, then each differing cluster's field changes. Shared by the report
// and --why.
func writeContentFinding(b *strings.Builder, f analyze.Finding) {
	d := f.Content
	reference := fmt.Sprintf("majority version on %d cluster(s): %s", len(d.MajorityClusters), strings.Join(d.MajorityClusters, ", "))
	if d.Ambiguous {
		reference = fmt.Sprintf("most common version on %d cluster(s): %s (no majority)", len(d.MajorityClusters), strings.Join(d.MajorityClusters, ", "))
	}
	fmt.Fprintf(b, "\n  %s — %s\n", f.Key.String(), reference)
	for _, cd := range d.Differing {
		fmt.Fprintf(b, "    cluster %s:\n", cd.Cluster)
		for _, op := range cd.Ops {
			fmt.Fprintf(b, "      %s: %s → %s\n", pointerToPath(op.Path), diffValue(op.Op, op.MajorityValue, "add"), diffValue(op.Op, op.ClusterValue, "remove"))
		}
	}
}

// mostDifferingFieldsString aggregates each changed path across all
// content-differs findings: "spec.template.spec.containers[0].image differs on
// 3 cluster(s)", most frequent first.
func mostDifferingFieldsString(differs []analyze.Finding) string {
	counts := map[string]int{}
	for _, f := range differs {
		for _, cd := range f.Content.Differing {
			for _, op := range cd.Ops {
				counts[pointerToPath(op.Path)]++
			}
		}
	}
	if len(counts) == 0 {
		return ""
	}
	type hot struct {
		path  string
		count int
	}
	hots := make([]hot, 0, len(counts))
	for p, c := range counts {
		hots = append(hots, hot{p, c})
	}
	sort.Slice(hots, func(i, j int) bool {
		if hots[i].count != hots[j].count {
			return hots[i].count > hots[j].count
		}
		return hots[i].path < hots[j].path
	})
	var b strings.Builder
	b.WriteString("\nFields that differ most:\n")
	for _, h := range hots {
		fmt.Fprintf(&b, "  %s differs on %d cluster(s)\n", h.path, h.count)
	}
	return b.String()
}

// pointerToPath renders an RFC 6901 JSON pointer as a friendlier dotted path:
// "/spec/template/spec/containers/0/image" → "spec.template.spec.containers[0].image".
func pointerToPath(ptr string) string {
	if ptr == "" {
		return "(root)"
	}
	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	var b strings.Builder
	for _, p := range parts {
		p = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
		if isAllDigits(p) {
			fmt.Fprintf(&b, "[%s]", p)
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(p)
	}
	return b.String()
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// diffValue formats one side of a diff, printing <absent> when the op type
// means this side has no value (add has no majority value; remove has no
// cluster value).
func diffValue(op string, v interface{}, absentFor string) string {
	if op == absentFor || v == nil {
		return "<absent>"
	}
	return fmt.Sprintf("%v", v)
}
