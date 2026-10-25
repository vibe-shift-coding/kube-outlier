package ownership

// Frozen marker constants (ADR-0005). Label/annotation keys and manager
// name prefixes that drive classification. Signal codes are frozen in
// docs/interfaces.md §4 and must match exactly.
const (
	labelHelmManagedBy = "app.kubernetes.io/managed-by"
	valueHelm          = "helm"

	annotationHelmReleaseName      = "meta.helm.sh/release-name"
	annotationHelmReleaseNamespace = "meta.helm.sh/release-namespace"

	managerHelm = "helm"

	annotationArgoTrackingID = "argocd.argoproj.io/tracking-id"
	managerPrefixArgo        = "argocd"

	labelFluxKustomizeName      = "kustomize.toolkit.fluxcd.io/name"
	labelFluxKustomizeNamespace = "kustomize.toolkit.fluxcd.io/namespace"
	labelFluxHelmName           = "helm.toolkit.fluxcd.io/name"
	labelFluxHelmNamespace      = "helm.toolkit.fluxcd.io/namespace"

	managerPrefixKubectl = "kubectl"

	annotationLastApplied = "kubectl.kubernetes.io/last-applied-configuration"
)

// fluxManagers is the set of managedFields manager names attributed to Flux
// controllers (ADR-0005: flux.manager).
var fluxManagers = map[string]bool{
	"kustomize-controller": true,
	"helm-controller":      true,
	"source-controller":    true,
}
