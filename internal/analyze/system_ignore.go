package analyze

import (
	"strings"

	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

// System objects are resources the Kubernetes control plane (or the cluster
// installer) creates itself. No pipeline deploys them and no human created
// them, but they carry no ownership markers, so without this filter they score
// as unmanaged — on a fresh cluster, --include-cluster-scoped alone surfaced
// ~180 of them (bootstrap RBAC, APIServices, API priority-and-fairness
// objects, system PriorityClasses and Namespaces, …). They are never findings,
// and never missing-from-cluster candidates (system objects legitimately
// differ between Kubernetes versions).
//
// This is tunable heuristic data — extend it as new universal false positives
// surface. Every rule below is void if any kubectl client wrote to the object:
// a human edit is evidence worth reporting.

// controlPlaneManagers are managedFields manager names used by in-tree
// control-plane components and kubeadm. An object written ONLY by these was
// created by the cluster itself.
var controlPlaneManagers = map[string]bool{
	"kube-apiserver":                     true,
	"kube-controller-manager":            true,
	"kube-scheduler":                     true,
	"kubelet":                            true,
	"kubeadm":                            true,
	"cloud-controller-manager":           true,
	"clusterrole-aggregation-controller": true, // aggregated admin/edit/view rules
	"service-cidr-controller":            true,
	"kubernetes-service-cidr-controller": true,
}

// controlPlaneManagerPrefixes cover in-tree managers whose names carry an API
// version suffix (e.g. "api-priority-and-fairness-config-producer-v1").
var controlPlaneManagerPrefixes = []string{
	"api-priority-and-fairness-config-",
}

// bootstrapLabels / bootstrapAnnotations are markers the API server puts on
// the defaults it creates. They identify system objects even where
// managedFields are absent (objects created before server-side field
// tracking, or stripped by a backup/restore).
var bootstrapLabels = map[string]string{
	"kubernetes.io/bootstrapping":               "rbac-defaults", // default ClusterRoles/Bindings
	"kube-aggregator.kubernetes.io/automanaged": "",              // built-in APIServices (any value)
}

var bootstrapAnnotations = []string{
	"apf.kubernetes.io/autoupdate-spec", // default FlowSchemas / PriorityLevelConfigurations
}

// systemIgnoredIdentities are system objects matched by identity, for
// clusters where the metadata rules above don't apply. The canonical case is
// the apiserver's own Service in the default namespace: unmanaged (+35) +
// Service kind-weight (+8) = 43, just over the default minScore of 40.
var systemIgnoredIdentities = map[identity.ResourceKey]bool{
	{Group: "", Kind: "Service", Namespace: "default", Name: "kubernetes"}: true,
}

// systemIgnoredInEveryNamespace are created by kube-controller-manager in
// every namespace (Namespace is ignored when matching). Unmanaged (+35) alone
// stays under the threshold, but once the namespace is ~30 days old the age
// band (+6) pushes each to 41, so every long-lived namespace would otherwise
// report both.
var systemIgnoredInEveryNamespace = map[identity.ResourceKey]bool{
	{Group: "", Kind: "ConfigMap", Name: "kube-root-ca.crt"}: true,
	{Group: "", Kind: "ServiceAccount", Name: "default"}:     true,
}

// isSystemIgnored reports whether k is a system object by identity alone.
func isSystemIgnored(k identity.ResourceKey) bool {
	if systemIgnoredIdentities[k] {
		return true
	}
	if k.Namespace == "" {
		return false
	}
	return systemIgnoredInEveryNamespace[identity.ResourceKey{Group: k.Group, Kind: k.Kind, Name: k.Name}]
}

// isSystemObject reports whether r was created by the control plane or the
// cluster installer and must never be surfaced as a finding.
func isSystemObject(r inventory.Resource) bool {
	if isSystemIgnored(r.Key) {
		return true
	}
	for _, mf := range r.ManagedFields {
		if strings.HasPrefix(mf.Manager, "kubectl") {
			return false // a human touched it
		}
	}
	for k, want := range bootstrapLabels {
		if v, ok := r.Labels[k]; ok && (want == "" || v == want) {
			return true
		}
	}
	for _, k := range bootstrapAnnotations {
		if _, ok := r.Annotations[k]; ok {
			return true
		}
	}
	if len(r.ManagedFields) == 0 {
		return false
	}
	for _, mf := range r.ManagedFields {
		if !isControlPlaneManager(mf.Manager) {
			return false
		}
	}
	return true
}

func isControlPlaneManager(m string) bool {
	if controlPlaneManagers[m] {
		return true
	}
	for _, p := range controlPlaneManagerPrefixes {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}

// systemObjectKeys returns every identity that is a system object on at least
// one of the snapshots, for checks that only have the identity to go on
// (missing-from-cluster, baseline entries).
func systemObjectKeys(snaps []*inventory.Snapshot) map[identity.ResourceKey]bool {
	keys := map[identity.ResourceKey]bool{}
	for _, s := range snaps {
		for _, r := range s.Resources {
			if isSystemObject(r) {
				keys[r.Key] = true
			}
		}
	}
	return keys
}

// IsSystemObject is the exported view of isSystemObject, used by --why to
// say WHY an identity was never a finding candidate.
func IsSystemObject(r inventory.Resource) bool {
	return isSystemObject(r)
}

// SystemObjectKeys is the exported view of systemObjectKeys, used by
// baseline generation so system objects never become baseline entries.
func SystemObjectKeys(snaps []*inventory.Snapshot) map[identity.ResourceKey]bool {
	return systemObjectKeys(snaps)
}
