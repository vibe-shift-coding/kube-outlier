package analyze_test

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
)

var fixedNow = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func sig(codes ...string) []ownership.Signal {
	out := make([]ownership.Signal, 0, len(codes))
	for _, c := range codes {
		out = append(out, ownership.Signal{Code: c})
	}
	return out
}

func resourceAt(kind string, created time.Time) inventory.Resource {
	return inventory.Resource{
		Key:               identity.ResourceKey{Kind: kind, Namespace: "default", Name: "x"},
		CreationTimestamp: metav1.NewTime(created),
	}
}

// Fixed score test cases (ADR-0002 sanity checks), fixed now = 2026-07-01T00:00:00Z.
func TestBaseScore_SpecTable(t *testing.T) {
	tests := []struct {
		name        string
		verdict     ownership.OwnershipVerdict
		resource    inventory.Resource
		wantReasons map[string]int // code -> points
		wantTotal   int
	}{
		{
			name: "1_unmanaged_kubectl_configmap_137d",
			verdict: ownership.OwnershipVerdict{
				KubectlWritten: true,
				Signals:        sig("kubectl.manager", "kubectl.last-applied", "no-pipeline-markers"),
			},
			resource:    resourceAt("ConfigMap", time.Date(2026, 2, 14, 9, 30, 0, 0, time.UTC)),
			wantReasons: map[string]int{"no-pipeline-markers": 35, "kubectl-manager": 25, "last-applied": 10, "age": 10},
			wantTotal:   80,
		},
		{
			name: "2_unmanaged_kubectl_create_networkpolicy_3d",
			verdict: ownership.OwnershipVerdict{
				KubectlWritten: true,
				Signals:        sig("kubectl.manager", "no-pipeline-markers"),
			},
			resource:    resourceAt("NetworkPolicy", time.Date(2026, 6, 28, 16, 12, 55, 0, time.UTC)),
			wantReasons: map[string]int{"no-pipeline-markers": 35, "kubectl-manager": 25, "kind-weight": 15},
			wantTotal:   75,
		},
		{
			name: "3_helm_deployment_old",
			verdict: ownership.OwnershipVerdict{
				KubectlWritten: false,
				Signals:        sig("helm.managed-by-label", "helm.release-annotation", "helm.manager"),
			},
			resource:    resourceAt("Deployment", time.Date(2025, 11, 12, 8, 14, 3, 0, time.UTC)),
			wantReasons: map[string]int{"kind-weight": 8, "age": 10},
			wantTotal:   18,
		},
		{
			name: "4_unknown_secret_45d",
			verdict: ownership.OwnershipVerdict{
				KubectlWritten: false,
				Signals:        sig("no-pipeline-markers"),
			},
			resource:    resourceAt("Secret", fixedNow.AddDate(0, 0, -45)),
			wantReasons: map[string]int{"no-pipeline-markers": 35, "kind-weight": 15, "age": 6},
			wantTotal:   56,
		},
		{
			name: "5_argocd_service_200d",
			verdict: ownership.OwnershipVerdict{
				KubectlWritten: false,
				Signals:        sig("argocd.tracking-id", "argocd.instance-label", "argocd.manager"),
			},
			resource:    resourceAt("Service", fixedNow.AddDate(0, 0, -200)),
			wantReasons: map[string]int{"kind-weight": 8, "age": 10},
			wantTotal:   18,
		},
		{
			name: "6_unmanaged_crd_instance_3d",
			verdict: ownership.OwnershipVerdict{
				KubectlWritten: false,
				Signals:        sig("no-pipeline-markers"),
			},
			resource:    resourceAt("SomeCustomResource", fixedNow.AddDate(0, 0, -3)),
			wantReasons: map[string]int{"no-pipeline-markers": 35},
			wantTotal:   35,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := analyze.BaseScore(tt.verdict, tt.resource, fixedNow)
			if got.Total != tt.wantTotal {
				t.Errorf("Total = %d, want %d (reasons: %+v)", got.Total, tt.wantTotal, got.Reasons)
			}
			if got.Base != got.Total {
				t.Errorf("Base (%d) should equal Total (%d) below the 100 clamp", got.Base, got.Total)
			}
			if got.FleetModifier != 0 || got.FleetApplied {
				t.Errorf("FleetModifier/FleetApplied must be zero-value in Tier 1, got %+v", got)
			}
			if len(got.Reasons) != len(tt.wantReasons) {
				t.Fatalf("Reasons = %+v, want codes %v", got.Reasons, tt.wantReasons)
			}
			for _, r := range got.Reasons {
				want, ok := tt.wantReasons[r.Code]
				if !ok {
					t.Errorf("unexpected reason code %q", r.Code)
					continue
				}
				if r.Points != want {
					t.Errorf("reason %q points = %d, want %d", r.Code, r.Points, want)
				}
			}
		})
	}
}

func TestBaseScore_AgeDetailFormat(t *testing.T) {
	verdict := ownership.OwnershipVerdict{KubectlWritten: true, Signals: sig("kubectl.manager", "kubectl.last-applied", "no-pipeline-markers")}
	resource := resourceAt("ConfigMap", time.Date(2026, 2, 14, 9, 30, 0, 0, time.UTC))

	got := analyze.BaseScore(verdict, resource, fixedNow)
	for _, r := range got.Reasons {
		if r.Code == "age" {
			if r.Detail != "137d" {
				t.Errorf("age Detail = %q, want %q", r.Detail, "137d")
			}
			return
		}
	}
	t.Fatal("no age reason found")
}
