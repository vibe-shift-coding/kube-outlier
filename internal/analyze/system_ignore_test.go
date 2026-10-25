package analyze

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

func TestIsSystemIgnored(t *testing.T) {
	tests := []struct {
		key  identity.ResourceKey
		want bool
	}{
		{identity.ResourceKey{Kind: "Service", Namespace: "default", Name: "kubernetes"}, true},
		{identity.ResourceKey{Kind: "Service", Namespace: "payments", Name: "kubernetes"}, false},
		// Created by kube-controller-manager in every namespace.
		{identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "kube-root-ca.crt"}, true},
		{identity.ResourceKey{Kind: "ConfigMap", Namespace: "payments", Name: "kube-root-ca.crt"}, true},
		{identity.ResourceKey{Kind: "ServiceAccount", Namespace: "payments", Name: "default"}, true},
		// Same names, different kinds or groups, are ordinary resources.
		{identity.ResourceKey{Kind: "ConfigMap", Namespace: "payments", Name: "default"}, false},
		{identity.ResourceKey{Kind: "Secret", Namespace: "payments", Name: "kube-root-ca.crt"}, false},
		{identity.ResourceKey{Group: "example.com", Kind: "ServiceAccount", Namespace: "payments", Name: "default"}, false},
	}
	for _, tc := range tests {
		if got := isSystemIgnored(tc.key); got != tc.want {
			t.Errorf("isSystemIgnored(%s) = %v, want %v", tc.key.String(), got, tc.want)
		}
	}
}

func TestIsSystemObject(t *testing.T) {
	mf := func(managers ...string) []metav1.ManagedFieldsEntry {
		var out []metav1.ManagedFieldsEntry
		for _, m := range managers {
			out = append(out, metav1.ManagedFieldsEntry{Manager: m})
		}
		return out
	}
	crb := identity.ResourceKey{Group: "rbac.authorization.k8s.io", Kind: "ClusterRoleBinding", Name: "x"}
	tests := []struct {
		name string
		r    inventory.Resource
		want bool
	}{
		{"written only by kube-apiserver", inventory.Resource{Key: crb, ManagedFields: mf("kube-apiserver")}, true},
		{"kube-apiserver + aggregation controller", inventory.Resource{Key: crb, ManagedFields: mf("kube-apiserver", "clusterrole-aggregation-controller")}, true},
		{"kubeadm bootstrap", inventory.Resource{Key: crb, ManagedFields: mf("kubeadm")}, true},
		{"APF producer (versioned manager name)", inventory.Resource{Key: crb, ManagedFields: mf("api-priority-and-fairness-config-producer-v1")}, true},
		{"rbac-defaults label, no managedFields", inventory.Resource{Key: crb, Labels: map[string]string{"kubernetes.io/bootstrapping": "rbac-defaults"}}, true},
		{"automanaged APIService label", inventory.Resource{Key: crb, Labels: map[string]string{"kube-aggregator.kubernetes.io/automanaged": "onstart"}}, true},
		{"APF autoupdate annotation", inventory.Resource{Key: crb, Annotations: map[string]string{"apf.kubernetes.io/autoupdate-spec": "true"}}, true},

		{"kubectl-created", inventory.Resource{Key: crb, ManagedFields: mf("kubectl-create")}, false},
		{"control plane but later kubectl-edited", inventory.Resource{Key: crb, ManagedFields: mf("kube-apiserver", "kubectl-edit")}, false},
		{"bootstrap label but kubectl-applied", inventory.Resource{Key: crb, Labels: map[string]string{"kubernetes.io/bootstrapping": "rbac-defaults"}, ManagedFields: mf("kubectl-client-side-apply")}, false},
		{"other bootstrapping value", inventory.Resource{Key: crb, Labels: map[string]string{"kubernetes.io/bootstrapping": "something-else"}}, false},
		{"control plane + an operator", inventory.Resource{Key: crb, ManagedFields: mf("kube-apiserver", "my-operator")}, false},
		{"operator only", inventory.Resource{Key: crb, ManagedFields: mf("cert-manager-controller")}, false},
		{"no managedFields, no markers", inventory.Resource{Key: crb}, false},
	}
	for _, tc := range tests {
		if got := isSystemObject(tc.r); got != tc.want {
			t.Errorf("%s: isSystemObject = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A system object on any cluster marks the identity for identity-only checks
// (missing-from-cluster): built-ins legitimately differ across versions.
func TestSystemObjectKeys(t *testing.T) {
	sys := identity.ResourceKey{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:new-in-1.36"}
	user := identity.ResourceKey{Kind: "ConfigMap", Namespace: "app", Name: "debug"}
	snaps := []*inventory.Snapshot{
		{Cluster: "a", Resources: []inventory.Resource{
			{Key: sys, ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kube-apiserver"}}},
			{Key: user, ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply"}}},
		}},
		{Cluster: "b", Resources: []inventory.Resource{
			{Key: user, ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply"}}},
		}},
	}
	got := systemObjectKeys(snaps)
	if !got[sys] || got[user] || len(got) != 1 {
		t.Errorf("systemObjectKeys = %v, want only %s", got, sys.String())
	}
}
