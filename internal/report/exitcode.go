package report

import (
	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
)

// ExitCode implements the interfaces.md §3b precedence exactly: 1 > 3 > 4 >
// 2 > 0. Code 1 (fatal) is handled upstream, before a Report even exists
// (bad flags, zero reachable clusters, ...) — this function only ever
// returns 0, 2, 3, or 4. With rc.AllowPartial a partial run that triggers
// nothing else returns 0 instead of 2.
func ExitCode(rep *Report, rc config.ReportConfig) int {
	wantInventory := rc.FailOn == config.FailOnInventory || rc.FailOn == config.FailOnAny
	wantContent := rc.FailOn == config.FailOnContent || rc.FailOn == config.FailOnAny

	if wantInventory && hasClass(rep.Findings, analyze.ClassLikelyManual, analyze.ClassMissingFromCluster) {
		return 3
	}
	if wantContent && hasClass(rep.Findings, analyze.ClassContentDiffers) {
		return 4
	}
	if isPartialRun(rep) && !rc.AllowPartial {
		return 2
	}
	return 0
}

func hasClass(findings []analyze.Finding, classes ...analyze.FindingClass) bool {
	want := make(map[analyze.FindingClass]bool, len(classes))
	for _, c := range classes {
		want[c] = true
	}
	for _, f := range findings {
		if want[f.Class] {
			return true
		}
	}
	return false
}

func isPartialRun(rep *Report) bool {
	return rep.Fleet.Reachable < rep.Fleet.Requested || len(rep.SkippedGVRs) > 0 || len(rep.SkippedObjects) > 0
}
