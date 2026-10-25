package report_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/report"
)

// A ConfigMap value can contain a markdown fence; the content block must
// still close where it ends, not at the value.
func TestRenderMarkdown_ContentFenceOutlastsBackticksInValues(t *testing.T) {
	f := analyze.Finding{
		Class:    analyze.ClassContentDiffers,
		Key:      identity.ResourceKey{Kind: "ConfigMap", Namespace: "docs", Name: "readme"},
		Clusters: []string{"b"},
		Score:    analyze.Score{Reasons: []analyze.ScoreReason{}},
		Content: &analyze.ContentDetail{
			MajorityClusters: []string{"a", "c"},
			Differing: []analyze.ClusterDifference{{Cluster: "b", Ops: []analyze.FieldOp{{
				Op: "replace", Path: "/data/README.md", MajorityValue: "plain", ClusterValue: "````\n# injected",
			}}}},
		},
	}
	rep := &report.Report{Findings: []analyze.Finding{f}, FleetPresence: report.FleetPresence{Applied: true, Source: "live"}}
	rep.Summary.Total = 1

	var buf bytes.Buffer
	if err := report.RenderMarkdown(&buf, rep); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "\n`````\nContent differences:") || !strings.HasSuffix(out, "`````\n") {
		t.Errorf("want a five-backtick fence around a value holding four, got:\n%s", out)
	}
}
