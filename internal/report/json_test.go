package report_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
	"github.com/vibe-shift-coding/kube-outlier/internal/report"
)

func TestRenderJSON_IndentAndTrailingNewline(t *testing.T) {
	r := &report.Report{
		SchemaVersion: 1,
		ToolVersion:   "v0.1.0-test",
		GeneratedAt:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		FleetPresence: report.FleetPresence{Applied: false, Source: "none"},
		Findings: []analyze.Finding{
			{
				Key:       identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "debug-dump"},
				Clusters:  []string{"cluster-eu1"},
				Ownership: &ownership.OwnershipVerdict{Owner: ownership.OwnerKubectl, KubectlWritten: true, Managers: []string{"kubectl-client-side-apply"}},
				Score:     analyze.Score{Base: 80, Total: 80},
			},
		},
	}

	var buf bytes.Buffer
	if err := report.RenderJSON(&buf, r); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	out := buf.String()

	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("output must end with a trailing newline, got %q", out[len(out)-10:])
	}
	if !strings.Contains(out, "\n  \"schemaVersion\"") {
		t.Errorf("want 2-space indent, got:\n%s", out)
	}

	var roundTrip map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &roundTrip); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	findings, ok := roundTrip["findings"].([]interface{})
	if !ok || len(findings) != 1 {
		t.Fatalf("want 1 finding in decoded JSON, got %+v", roundTrip["findings"])
	}
	resource, ok := findings[0].(map[string]interface{})["resource"].(map[string]interface{})
	if !ok {
		t.Fatalf("finding missing resource object: %+v", findings[0])
	}
	for _, key := range []string{"group", "kind", "namespace", "name"} {
		if _, ok := resource[key]; !ok {
			t.Errorf("resource missing camelCase key %q: %+v", key, resource)
		}
	}
	if resource["kind"] != "ConfigMap" {
		t.Errorf("resource[kind] = %v, want ConfigMap", resource["kind"])
	}
}
