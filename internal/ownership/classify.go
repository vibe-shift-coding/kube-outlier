// Package ownership classifies each Resource's likely creator from metadata
// alone (Tier 1; rules frozen in ADR-0005).
package ownership

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

// Owner is the likely creator of a resource, by precedence (ADR-0005).
type Owner string

// The Owner values. Controller means non-empty ownerReferences; Kubectl
// means a kubectl client wrote it and no pipeline marker is present.
const (
	OwnerHelm       Owner = "helm"
	OwnerArgoCD     Owner = "argocd"
	OwnerFlux       Owner = "flux"
	OwnerController Owner = "controller" // non-empty ownerReferences
	OwnerKubectl    Owner = "kubectl"
	OwnerUnknown    Owner = "unknown"
)

// Signal is one piece of classification evidence. Code values are a frozen
// enum — see interfaces.md §4. Detail is free-form human text.
type Signal struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// OwnershipVerdict is the full ownership-signal classification for one Resource.
type OwnershipVerdict struct {
	Owner          Owner    `json:"owner"`          // primary owner by precedence: controller > flux > argocd > helm > kubectl > unknown
	KubectlWritten bool     `json:"kubectlWritten"` // true iff any managedFields manager has prefix "kubectl" (the client, not necessarily a person)
	Managers       []string `json:"managers"`       // distinct managedFields manager names, sorted
	Signals        []Signal `json:"signals"`        // every signal that fired, stable order (marker table order)
}

// Classify implements the ADR-0005 marker table verbatim: signal order =
// table order, owner precedence controller > flux > argocd > helm > kubectl
// > unknown, argocd.instance-label alone never establishes argocd ownership,
// KubectlWritten iff any manager has prefix "kubectl".
func Classify(r inventory.Resource, cfg config.AnalysisConfig) OwnershipVerdict {
	managers := managerNames(r)

	hasManagerWithPrefix := func(prefix string) (string, bool) {
		for _, m := range managers {
			if strings.HasPrefix(m, prefix) {
				return m, true
			}
		}
		return "", false
	}
	hasExactManager := func(name string) bool {
		for _, m := range managers {
			if m == name {
				return true
			}
		}
		return false
	}

	instanceLabelKey := cfg.ArgoInstanceLabelKey
	if instanceLabelKey == "" {
		instanceLabelKey = "app.kubernetes.io/instance"
	}

	var signals []Signal

	// Helm family.
	helmFired := false
	if v, ok := r.Labels[labelHelmManagedBy]; ok && strings.EqualFold(v, valueHelm) {
		signals = append(signals, Signal{Code: "helm.managed-by-label", Detail: fmt.Sprintf("%s=%s", labelHelmManagedBy, v)})
		helmFired = true
	}
	if _, ok := r.Annotations[annotationHelmReleaseName]; ok {
		signals = append(signals, Signal{Code: "helm.release-annotation", Detail: annotationHelmReleaseName + " present"})
		helmFired = true
	} else if _, ok := r.Annotations[annotationHelmReleaseNamespace]; ok {
		signals = append(signals, Signal{Code: "helm.release-annotation", Detail: annotationHelmReleaseNamespace + " present"})
		helmFired = true
	}
	if hasExactManager(managerHelm) {
		signals = append(signals, Signal{Code: "helm.manager", Detail: "manager=" + managerHelm})
		helmFired = true
	}

	// Argo family.
	argoQualifies := false
	if _, ok := r.Annotations[annotationArgoTrackingID]; ok {
		signals = append(signals, Signal{Code: "argocd.tracking-id", Detail: annotationArgoTrackingID + " present"})
		argoQualifies = true
	}
	instanceLabelFired := false
	if _, ok := r.Labels[instanceLabelKey]; ok {
		signals = append(signals, Signal{Code: "argocd.instance-label", Detail: instanceLabelKey + " present"})
		instanceLabelFired = true
	}
	if m, ok := hasManagerWithPrefix(managerPrefixArgo); ok {
		signals = append(signals, Signal{Code: "argocd.manager", Detail: "manager=" + m})
		argoQualifies = true
	}

	// Flux family.
	fluxFired := false
	if _, ok := r.Labels[labelFluxKustomizeName]; ok {
		if _, ok2 := r.Labels[labelFluxKustomizeNamespace]; ok2 {
			signals = append(signals, Signal{Code: "flux.kustomize-label", Detail: labelFluxKustomizeName + "+" + labelFluxKustomizeNamespace + " present"})
			fluxFired = true
		}
	}
	if _, ok := r.Labels[labelFluxHelmName]; ok {
		if _, ok2 := r.Labels[labelFluxHelmNamespace]; ok2 {
			signals = append(signals, Signal{Code: "flux.helm-label", Detail: labelFluxHelmName + "+" + labelFluxHelmNamespace + " present"})
			fluxFired = true
		}
	}
	if m, ok := findManager(managers, func(m string) bool { return fluxManagers[m] }); ok {
		signals = append(signals, Signal{Code: "flux.manager", Detail: "manager=" + m})
		fluxFired = true
	}

	// Controller.
	controllerFired := len(r.OwnerReferences) > 0
	if controllerFired {
		signals = append(signals, Signal{Code: "controller.owner-references", Detail: fmt.Sprintf("%d ownerReference(s)", len(r.OwnerReferences))})
	}

	// kubectl.
	kubectlManagerFired := false
	if m, ok := hasManagerWithPrefix(managerPrefixKubectl); ok {
		signals = append(signals, Signal{Code: "kubectl.manager", Detail: "manager=" + m})
		kubectlManagerFired = true
	}
	if _, ok := r.Annotations[annotationLastApplied]; ok {
		signals = append(signals, Signal{Code: "kubectl.last-applied", Detail: annotationLastApplied + " present"})
	}

	// no-pipeline-markers: no Helm/Argo/Flux family fired (the instance label counts
	// as an Argo-family marker here even though it doesn't alone establish
	// argocd ownership) AND no ownerReferences.
	anyPipelineMarker := helmFired || argoQualifies || instanceLabelFired || fluxFired
	if !anyPipelineMarker && !controllerFired {
		signals = append(signals, Signal{Code: "no-pipeline-markers", Detail: "no Helm, Argo CD or Flux markers, no ownerReferences"})
	}

	owner := OwnerUnknown
	switch {
	case controllerFired:
		owner = OwnerController
	case fluxFired:
		owner = OwnerFlux
	case argoQualifies:
		owner = OwnerArgoCD
	case helmFired:
		owner = OwnerHelm
	case kubectlManagerFired:
		owner = OwnerKubectl
	}

	return OwnershipVerdict{
		Owner:          owner,
		KubectlWritten: kubectlManagerFired,
		Managers:       managers,
		Signals:        signals,
	}
}

func managerNames(r inventory.Resource) []string {
	seen := make(map[string]bool, len(r.ManagedFields))
	var out []string
	for _, mf := range r.ManagedFields {
		if mf.Manager == "" || seen[mf.Manager] {
			continue
		}
		seen[mf.Manager] = true
		out = append(out, mf.Manager)
	}
	sort.Strings(out)
	return out
}

func findManager(managers []string, match func(string) bool) (string, bool) {
	for _, m := range managers {
		if match(m) {
			return m, true
		}
	}
	return "", false
}
