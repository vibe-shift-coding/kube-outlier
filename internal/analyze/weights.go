package analyze

// Every number in this file comes from ADR-0002 and lives ONLY here — do not
// inline scoring numbers anywhere else. Tuning changes touch this file plus
// the ADR, nothing else.

const (
	// noPipelineMarkersPoints fires when no Helm/Argo/Flux markers fired AND
	// there are no ownerReferences (ownership.Signal "no-pipeline-markers"):
	// the strongest single signal that a resource was never touched by any
	// pipeline.
	noPipelineMarkersPoints = 35

	// kubectlManagerPoints fires when any managedFields manager has prefix
	// "kubectl" (ownership.OwnershipVerdict.KubectlWritten): direct evidence a
	// kubectl client wrote to this object, independent of
	// whatever else also manages it (e.g. a manual `helm install`).
	kubectlManagerPoints = 25

	// lastAppliedPoints fires when the kubectl.kubernetes.io/last-applied-
	// configuration annotation is present: corroborates kubectl authorship
	// (note `kubectl create` does not set this, so it's additive, not
	// required, evidence).
	lastAppliedPoints = 10

	// kindWeightHighPoints applies to kinds where a stray one-off is
	// unusually consequential (RBAC escalation, network policy bypass,
	// webhook interference, secret sprawl).
	kindWeightHighPoints = 15

	// kindWeightMediumPoints applies to common workload/networking kinds:
	// still worth flagging, but a one-off Deployment is lower blast-radius
	// than a one-off ClusterRoleBinding.
	kindWeightMediumPoints = 8

	// Age bands: age = now − creationTimestamp (calendar days). An old
	// unmanaged resource is more likely truly forgotten than one created
	// minutes ago mid-incident.
	ageUnder7dPoints = 0
	age7to30dPoints  = 3
	age30to90dPoints = 6
	ageOver90dPoints = 10

	// Fleet presence modifiers (ADR-0002). fleetRarePoints fires when the
	// presence ratio r <= analysis.rareThreshold: un-annotated AND rare is the
	// strongest combined "forgotten one-off" evidence. fleetCommonPoints fires
	// when r == 1.0: present on every cluster means probably a legitimate
	// un-labelled resource, so the score is lowered (but strong kubectl
	// evidence everywhere still keeps a finding visible: 95 − 30 = 65).
	fleetRarePoints   = 20
	fleetCommonPoints = -30

	// fixedClassScore is the constant Total for missing-from-cluster, which
	// carries no per-resource evidence to weigh: the anomaly IS the finding. 50 sits above the
	// default minScore of 40 but below any hand-applied high-evidence score, so
	// these surface by default without outranking direct kubectl evidence
	// (ADR-0002).
	fixedClassScore = 50
)

// kindWeightHigh is the ADR-0002 HIGH set: a manual one-off here has outsized
// blast radius (privilege escalation, bypassed network isolation, webhook
// interference, secret sprawl).
var kindWeightHigh = map[string]bool{
	"Role":                           true,
	"RoleBinding":                    true,
	"ClusterRole":                    true,
	"ClusterRoleBinding":             true,
	"NetworkPolicy":                  true,
	"Secret":                         true,
	"ValidatingWebhookConfiguration": true,
	"MutatingWebhookConfiguration":   true,
}

// kindWeightMedium is the ADR-0002 MEDIUM set: common workload/networking
// kinds, still worth flagging but lower blast-radius than the HIGH set.
var kindWeightMedium = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"CronJob":     true,
	"Job":         true,
	"Service":     true,
	"Ingress":     true,
}
