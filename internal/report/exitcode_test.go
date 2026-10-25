package report_test

import (
	"testing"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/report"
)

func rep(requested, reachable int, skippedGVRs int, findings ...analyze.Finding) *report.Report {
	r := &report.Report{
		Fleet:    report.FleetSummary{Requested: requested, Reachable: reachable},
		Findings: findings,
	}
	for i := 0; i < skippedGVRs; i++ {
		r.SkippedGVRs = append(r.SkippedGVRs, report.SkippedGVR{Cluster: "c", GVR: "v1/secrets", Forbidden: true, Reason: "forbidden"})
	}
	return r
}

func finding(class analyze.FindingClass) analyze.Finding {
	return analyze.Finding{Class: class}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		name   string
		report *report.Report
		failOn config.FailOn
		want   int
	}{
		{"no findings none skipped fail none", rep(1, 1, 0), config.FailOnNone, 0},
		{"findings fail none", rep(1, 1, 0, finding(analyze.ClassLikelyManual)), config.FailOnNone, 0},
		{"findings fail inventory", rep(1, 1, 0, finding(analyze.ClassLikelyManual)), config.FailOnInventory, 3},
		{"findings and skipped cluster fail inventory", rep(2, 1, 0, finding(analyze.ClassLikelyManual)), config.FailOnInventory, 3},
		{"no findings skipped gvr fail any", rep(1, 1, 1), config.FailOnAny, 2},
		{"content finding fail content", rep(1, 1, 0, finding(analyze.ClassContentDiffers)), config.FailOnContent, 4},
		{"content and inventory fail any", rep(1, 1, 0, finding(analyze.ClassContentDiffers), finding(analyze.ClassLikelyManual)), config.FailOnAny, 3},
		{"skipped cluster only fail none", rep(2, 1, 0), config.FailOnNone, 2},
		{"missing-from-cluster fail inventory", rep(3, 3, 0, finding(analyze.ClassMissingFromCluster)), config.FailOnInventory, 3},
		{"missing-from-cluster fail content only", rep(3, 3, 0, finding(analyze.ClassMissingFromCluster)), config.FailOnContent, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := report.ExitCode(tt.report, config.ReportConfig{FailOn: tt.failOn})
			if got != tt.want {
				t.Errorf("ExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

// AllowPartial only replaces code 2; findings that trigger failOn still win.
func TestExitCode_AllowPartial(t *testing.T) {
	tests := []struct {
		name   string
		report *report.Report
		failOn config.FailOn
		want   int
	}{
		{"skipped gvr", rep(1, 1, 1), config.FailOnNone, 0},
		{"skipped cluster", rep(2, 1, 0), config.FailOnAny, 0},
		{"skipped cluster with inventory", rep(2, 1, 0, finding(analyze.ClassLikelyManual)), config.FailOnInventory, 3},
		{"skipped gvr with content", rep(1, 1, 1, finding(analyze.ClassContentDiffers)), config.FailOnContent, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := report.ExitCode(tt.report, config.ReportConfig{FailOn: tt.failOn, AllowPartial: true})
			if got != tt.want {
				t.Errorf("ExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

// A copy the content comparison could not read makes the run partial: it
// exits 2 unless allowPartial, content differences still win, and stderr
// names the copy.
func TestExitCode_UnreadObjectIsPartial(t *testing.T) {
	r := rep(3, 3, 0)
	r.SkippedObjects = []report.SkippedObject{{
		Cluster:   "c",
		Resource:  identity.ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "app", Name: "web"},
		Reason:    "forbidden",
		Forbidden: true,
	}}
	if got := report.ExitCode(r, config.ReportConfig{FailOn: config.FailOnContent}); got != 2 {
		t.Errorf("unread copy: ExitCode() = %d, want 2", got)
	}
	if got := report.ExitCode(r, config.ReportConfig{FailOn: config.FailOnContent, AllowPartial: true}); got != 0 {
		t.Errorf("unread copy with allowPartial: ExitCode() = %d, want 0", got)
	}
	r.Findings = []analyze.Finding{finding(analyze.ClassContentDiffers)}
	if got := report.ExitCode(r, config.ReportConfig{FailOn: config.FailOnContent}); got != 4 {
		t.Errorf("unread copy with a content difference: ExitCode() = %d, want 4", got)
	}
	lines := report.SkipSummaryLines(r)
	if len(lines) != 1 || lines[0] != "c: get apps/Deployment app/web forbidden, left out of the content comparison" {
		t.Errorf("SkipSummaryLines = %q", lines)
	}
}
