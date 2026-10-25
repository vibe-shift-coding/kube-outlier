package report

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
)

// RenderTable writes the human table to w: header always printed, one row
// per finding, sorted in rep.Findings' order (Score.Total desc, Key asc).
// When the fleet presence signal applied a PRESENT-ON column is inserted
// after NAMESPACE/NAME; otherwise the column is omitted — presence data that
// doesn't exist is never faked. An empty report is just the header; the
// caller prints the "No findings." notice (to stderr, so stdout stays data).
func RenderTable(w io.Writer, rep *Report) error {
	return RenderTableWidth(w, rep, 0)
}

// RenderTableWidth is RenderTable for a terminal width columns wide (0 =
// unknown, never narrow). When the table would be wider than that, each
// row's WHY codes move to an indented line of their own below it, so the
// other columns stay aligned instead of wrapping.
func RenderTableWidth(w io.Writer, rep *Report, width int) error {
	var buf bytes.Buffer
	if err := writeRows(&buf, rep); err != nil {
		return err
	}
	if width > 0 && len(rep.Findings) > 0 && widestLine(buf.String()) > width {
		buf.Reset()
		if err := writeNarrowRows(&buf, rep); err != nil {
			return err
		}
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}

	if detail := contentDetailString(rep); detail != "" {
		if _, err := io.WriteString(w, detail); err != nil {
			return err
		}
	}
	return nil
}

// writeRows writes the header and one tab-aligned line per finding.
func writeRows(w io.Writer, rep *Report) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(headerCells(rep.FleetPresence), "\t")); err != nil {
		return err
	}
	for _, f := range rep.Findings {
		if _, err := fmt.Fprintln(tw, strings.Join(findingCells(f, rep.FleetPresence.Applied), "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// whyIndent lines a narrow row's WHY line up under KIND: the SCORE column is
// "SCORE" plus the two-space column gap wide.
const whyIndent = "       "

// writeNarrowRows writes the table without its WHY column, each row followed
// by an indented "why:" line. The columns are aligned first and the WHY lines
// added after, so they don't break tabwriter's column blocks.
func writeNarrowRows(w io.Writer, rep *Report) error {
	header := headerCells(rep.FleetPresence)
	var aligned bytes.Buffer
	tw := tabwriter.NewWriter(&aligned, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(header[:len(header)-1], "\t")); err != nil {
		return err
	}
	for _, f := range rep.Findings {
		cells := findingCells(f, rep.FleetPresence.Applied)
		if _, err := fmt.Fprintln(tw, strings.Join(cells[:len(cells)-1], "\t")); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(aligned.String(), "\n"), "\n")
	var b strings.Builder
	b.WriteString(lines[0] + "\n")
	for i, f := range rep.Findings {
		cells := findingCells(f, rep.FleetPresence.Applied)
		why := strings.ReplaceAll(cells[len(cells)-1], ",", ", ")
		fmt.Fprintf(&b, "%s\n%swhy: %s\n", lines[i+1], whyIndent, why)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// widestLine returns the length in runes of the longest line in s.
func widestLine(s string) int {
	widest := 0
	for _, line := range strings.Split(s, "\n") {
		widest = max(widest, utf8.RuneCountInString(line))
	}
	return widest
}

// headerCells is the column set, shared by the tab table and the markdown
// table so the two renderers can never drift apart. SCORE comes first: it is
// the sort key, and it stays visible when a narrow terminal wraps the rest.
// The presence column is PRESENT-ON for a live fleet and IN-BASELINE against
// a baseline, where the count is of baseline clusters, not including the one
// being scanned. WHY is always last.
func headerCells(p FleetPresence) []string {
	if !p.Applied {
		return []string{"SCORE", "KIND", "NAMESPACE/NAME", "CLUSTER", "OWNER", "AGE", "WHY"}
	}
	presence := "PRESENT-ON"
	if p.Source == "baseline" {
		presence = "IN-BASELINE"
	}
	return []string{"SCORE", "KIND", "NAMESPACE/NAME", presence, "CLUSTER", "OWNER", "AGE", "WHY"}
}

func findingCells(f analyze.Finding, enriched bool) []string {
	name := f.Key.Name
	if f.Key.Namespace != "" {
		name = f.Key.Namespace + "/" + f.Key.Name
	}

	cluster := strings.Join(f.Clusters, ",")

	// WHY = comma-joined score reason codes; missing-from-cluster and
	// content-differs have no reasons (ADR-0002), so the class name says why.
	why := string(f.Class)
	if len(f.Score.Reasons) > 0 {
		codes := make([]string, 0, len(f.Score.Reasons))
		for _, r := range f.Score.Reasons {
			codes = append(codes, r.Code)
		}
		why = strings.Join(codes, ",")
	}

	// content-differs findings carry no score (ADR-0006): SCORE renders "-".
	score := fmt.Sprintf("%d", f.Score.Total)
	if f.Class == analyze.ClassContentDiffers {
		score = "-"
	}
	cells := []string{score, kindCell(f.Key), name}
	if enriched {
		presentOn := "-"
		if f.Presence != nil {
			presentOn = fmt.Sprintf("%d/%d", f.Presence.PresentCount, f.Presence.FleetSize)
		}
		cells = append(cells, presentOn)
	}
	age := "-"
	if f.AgeDays != nil {
		age = humanizeAge(*f.AgeDays)
	}
	return append(cells, cluster, OwnerCell(f.Ownership), age, why)
}

// kindCell renders the canonical "group/Kind" (bare Kind for the core group),
// so a row's KIND and NAMESPACE/NAME paste straight into --why.
func kindCell(k identity.ResourceKey) string {
	if k.Group == "" {
		return k.Kind
	}
	return k.Group + "/" + k.Kind
}

// OwnerCell renders the OWNER column: the primary owner, plus "+kubectl" when
// a kubectl client also wrote an object that carries pipeline markers
// ("helm+kubectl"). Owner kubectl already says so. "-" when no single object
// was classified (missing-from-cluster, content-differs).
func OwnerCell(v *ownership.OwnershipVerdict) string {
	if v == nil {
		return "-"
	}
	if v.KubectlWritten && v.Owner != ownership.OwnerKubectl {
		return string(v.Owner) + "+kubectl"
	}
	return string(v.Owner)
}

// humanizeAge renders whole days for anything under a year ("137d"), and
// whole years beyond that ("2y") — the table has no room for finer detail.
func humanizeAge(days int) string {
	if days < 365 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dy", days/365)
}
