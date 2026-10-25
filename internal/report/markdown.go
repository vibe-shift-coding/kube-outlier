package report

import (
	"fmt"
	"io"
	"strings"
)

// RenderMarkdown emits the findings as a GitHub-flavored markdown report:
// a header with provenance, the same column set as RenderTable
// (including the fleet-presence-conditional PRESENT-ON), a summary line, and
// what was skipped, if anything.
// Unlike the terminal table, the empty-scan notice goes into the body, not
// stderr — a pasted report must be self-contained.
func RenderMarkdown(w io.Writer, rep *Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "## kube-outlier scan\n\n")
	fmt.Fprintf(&b, "`%s` — generated %s\n\n", rep.ToolVersion, rep.GeneratedAt.Format("2006-01-02 15:04:05 UTC"))

	if len(rep.Findings) == 0 {
		b.WriteString("No findings.\n")
	} else {
		writeMarkdownRow(&b, headerCells(rep.FleetPresence))
		sep := make([]string, len(headerCells(rep.FleetPresence)))
		for i := range sep {
			sep[i] = "---"
		}
		writeMarkdownRow(&b, sep)
		for _, f := range rep.Findings {
			cells := findingCells(f, rep.FleetPresence.Applied)
			// Spaced WHY codes let a rendered table wrap the column.
			cells[len(cells)-1] = strings.ReplaceAll(cells[len(cells)-1], ",", ", ")
			writeMarkdownRow(&b, cells)
		}
		b.WriteString("\n")
	}

	b.WriteString(SummaryLine(rep) + "\n")

	// What was skipped, so a pasted report says it is partial.
	if skipped := SkipSummaryLines(rep); len(skipped) > 0 {
		b.WriteString("\nSkipped, so not covered by this report:\n\n")
		for _, line := range skipped {
			fmt.Fprintf(&b, "- `%s`\n", strings.ReplaceAll(line, "`", "'"))
		}
	}

	// Content differences as a fenced block — the field diffs and hot-field summary
	// don't fit a table, and a pasted markdown report must be self-contained.
	// The fence is longer than any backtick run in the content, so a value
	// containing ``` cannot end the block early.
	if detail := contentDetailString(rep); detail != "" {
		fence := codeFence(detail)
		b.WriteString("\n" + fence)
		b.WriteString(detail)
		b.WriteString(fence + "\n")
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// writeMarkdownRow escapes pipes (the only markdown-table metacharacter our
// cell values can contain) and writes one table row.
func writeMarkdownRow(b *strings.Builder, cells []string) {
	escaped := make([]string, len(cells))
	for i, c := range cells {
		escaped[i] = strings.ReplaceAll(c, "|", "\\|")
	}
	fmt.Fprintf(b, "| %s |\n", strings.Join(escaped, " | "))
}

// codeFence returns a backtick fence for content: three backticks, or one
// more than the longest backtick run inside it (CommonMark: a fenced block
// ends only at a fence at least as long as the opening one).
func codeFence(content string) string {
	longest, run := 0, 0
	for _, c := range content {
		if c == '`' {
			run++
			longest = max(longest, run)
			continue
		}
		run = 0
	}
	return strings.Repeat("`", max(3, longest+1))
}
