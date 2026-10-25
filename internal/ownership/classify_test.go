package ownership_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
)

func managedBy(managers ...string) []metav1.ManagedFieldsEntry {
	out := make([]metav1.ManagedFieldsEntry, 0, len(managers))
	for _, m := range managers {
		out = append(out, metav1.ManagedFieldsEntry{Manager: m})
	}
	return out
}

func signalCodes(signals []ownership.Signal) []string {
	out := make([]string, 0, len(signals))
	for _, s := range signals {
		out = append(out, s.Code)
	}
	return out
}

func defaultCfg() config.AnalysisConfig {
	return config.AnalysisConfig{ArgoInstanceLabelKey: "app.kubernetes.io/instance"}
}

// The 12-case classification table (ADR-0005 marker table). Signal order
// asserted here follows ADR-0005's literal table order (helm, argo, flux,
// controller, kubectl, unmanaged).
func TestClassify_TwelveCaseTable(t *testing.T) {
	tests := []struct {
		name               string
		resource           inventory.Resource
		cfg                config.AnalysisConfig
		wantOwner          ownership.Owner
		wantKubectlWritten bool
		wantSignals        []string
	}{
		{
			name: "1_helm_full_markers",
			resource: inventory.Resource{
				Labels:        map[string]string{"app.kubernetes.io/managed-by": "Helm"},
				Annotations:   map[string]string{"meta.helm.sh/release-name": "myrelease"},
				ManagedFields: managedBy("helm"),
			},
			cfg:                defaultCfg(),
			wantOwner:          ownership.OwnerHelm,
			wantKubectlWritten: false,
			wantSignals:        []string{"helm.managed-by-label", "helm.release-annotation", "helm.manager"},
		},
		{
			name: "2_manual_helm_install",
			resource: inventory.Resource{
				Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"},
				Annotations: map[string]string{
					"meta.helm.sh/release-name":                        "myrelease",
					"kubectl.kubernetes.io/last-applied-configuration": "{}",
				},
				ManagedFields: managedBy("kubectl-client-side-apply"),
			},
			cfg:                defaultCfg(),
			wantOwner:          ownership.OwnerHelm,
			wantKubectlWritten: true,
			wantSignals:        []string{"helm.managed-by-label", "helm.release-annotation", "kubectl.manager", "kubectl.last-applied"},
		},
		{
			name: "3_argocd_synced",
			resource: inventory.Resource{
				Labels:        map[string]string{"app.kubernetes.io/instance": "myapp"},
				Annotations:   map[string]string{"argocd.argoproj.io/tracking-id": "myapp:/Service:ns/myapp"},
				ManagedFields: managedBy("argocd-application-controller"),
			},
			cfg:                defaultCfg(),
			wantOwner:          ownership.OwnerArgoCD,
			wantKubectlWritten: false,
			wantSignals:        []string{"argocd.tracking-id", "argocd.instance-label", "argocd.manager"},
		},
		{
			name: "4_instance_label_only_helm_chart_style",
			resource: inventory.Resource{
				Labels:        map[string]string{"app.kubernetes.io/instance": "myapp"},
				ManagedFields: managedBy("helm"),
			},
			cfg:                defaultCfg(),
			wantOwner:          ownership.OwnerHelm,
			wantKubectlWritten: false,
			wantSignals:        []string{"helm.manager", "argocd.instance-label"},
		},
		{
			name: "5_instance_label_only_no_managers_of_interest",
			resource: inventory.Resource{
				Labels: map[string]string{"app.kubernetes.io/instance": "myapp"},
			},
			cfg:                defaultCfg(),
			wantOwner:          ownership.OwnerUnknown,
			wantKubectlWritten: false,
			wantSignals:        []string{"argocd.instance-label"},
		},
		{
			name: "6_flux_kustomize",
			resource: inventory.Resource{
				Labels: map[string]string{
					"kustomize.toolkit.fluxcd.io/name":      "myapp",
					"kustomize.toolkit.fluxcd.io/namespace": "flux-system",
				},
				ManagedFields: managedBy("kustomize-controller"),
			},
			cfg:                defaultCfg(),
			wantOwner:          ownership.OwnerFlux,
			wantKubectlWritten: false,
			wantSignals:        []string{"flux.kustomize-label", "flux.manager"},
		},
		{
			name: "7_flux_helmrelease_output",
			resource: inventory.Resource{
				Labels: map[string]string{
					"helm.toolkit.fluxcd.io/name":      "myapp",
					"helm.toolkit.fluxcd.io/namespace": "flux-system",
				},
				Annotations:   map[string]string{"meta.helm.sh/release-name": "myapp"},
				ManagedFields: managedBy("helm-controller"),
			},
			cfg:                defaultCfg(),
			wantOwner:          ownership.OwnerFlux,
			wantKubectlWritten: false,
			wantSignals:        []string{"helm.release-annotation", "flux.helm-label", "flux.manager"},
		},
		{
			name: "8_controller_owned",
			resource: inventory.Resource{
				OwnerReferences: []metav1.OwnerReference{{Kind: "SomeOperator", Name: "x"}},
				ManagedFields:   managedBy("kubectl-client-side-apply"),
			},
			cfg:                defaultCfg(),
			wantOwner:          ownership.OwnerController,
			wantKubectlWritten: true,
			wantSignals:        []string{"controller.owner-references", "kubectl.manager"},
		},
		{
			name: "9_kubectl_apply_hand_applied",
			resource: inventory.Resource{
				Annotations:   map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}"},
				ManagedFields: managedBy("kubectl-client-side-apply"),
			},
			cfg:                defaultCfg(),
			wantOwner:          ownership.OwnerKubectl,
			wantKubectlWritten: true,
			wantSignals:        []string{"kubectl.manager", "kubectl.last-applied", "no-pipeline-markers"},
		},
		{
			name: "10_kubectl_create_no_last_applied",
			resource: inventory.Resource{
				ManagedFields: managedBy("kubectl-create"),
			},
			cfg:                defaultCfg(),
			wantOwner:          ownership.OwnerKubectl,
			wantKubectlWritten: true,
			wantSignals:        []string{"kubectl.manager", "no-pipeline-markers"},
		},
		{
			name: "11_fully_unknown",
			resource: inventory.Resource{
				ManagedFields: managedBy("some-operator"),
			},
			cfg:                defaultCfg(),
			wantOwner:          ownership.OwnerUnknown,
			wantKubectlWritten: false,
			wantSignals:        []string{"no-pipeline-markers"},
		},
		{
			name: "12_custom_argo_instance_label_key",
			resource: inventory.Resource{
				Labels:      map[string]string{"argocd.example.com/app": "myapp"},
				Annotations: map[string]string{"argocd.argoproj.io/tracking-id": "myapp:/Service:ns/myapp"},
			},
			cfg:                config.AnalysisConfig{ArgoInstanceLabelKey: "argocd.example.com/app"},
			wantOwner:          ownership.OwnerArgoCD,
			wantKubectlWritten: false,
			wantSignals:        []string{"argocd.tracking-id", "argocd.instance-label"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ownership.Classify(tt.resource, tt.cfg)
			if got.Owner != tt.wantOwner {
				t.Errorf("Owner = %v, want %v", got.Owner, tt.wantOwner)
			}
			if got.KubectlWritten != tt.wantKubectlWritten {
				t.Errorf("KubectlWritten = %v, want %v", got.KubectlWritten, tt.wantKubectlWritten)
			}
			gotCodes := signalCodes(got.Signals)
			if len(gotCodes) != len(tt.wantSignals) {
				t.Fatalf("Signals = %v, want %v", gotCodes, tt.wantSignals)
			}
			for i, code := range tt.wantSignals {
				if gotCodes[i] != code {
					t.Errorf("Signals[%d] = %q, want %q (full got: %v)", i, gotCodes[i], code, gotCodes)
				}
			}
		})
	}
}

func TestClassify_ManagersSortedAndDistinct(t *testing.T) {
	r := inventory.Resource{
		ManagedFields: managedBy("kubectl-edit", "helm", "kubectl-edit", "argocd-application-controller"),
	}
	got := ownership.Classify(r, defaultCfg())
	want := []string{"argocd-application-controller", "helm", "kubectl-edit"}
	if len(got.Managers) != len(want) {
		t.Fatalf("Managers = %v, want %v", got.Managers, want)
	}
	for i, m := range want {
		if got.Managers[i] != m {
			t.Errorf("Managers[%d] = %q, want %q", i, got.Managers[i], m)
		}
	}
}
