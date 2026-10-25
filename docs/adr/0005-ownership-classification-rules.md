# ADR-0005: Ownership classification — exact markers, precedence, and the Argo instance-label caveat

## Context
Ownership markers come in several families, and many resources match more than one
(common: Helm annotations + kubectl managedFields from a manual `helm install`, or
Argo-synced objects that also carry Helm labels). The label `app.kubernetes.io/instance` is
also ambiguous. Classification needs exact markers, a precedence rule, and a decision on
that label.

## Decision

**Marker → signal table (frozen; codes from interfaces.md §4):**

| family | fires signal | condition |
|--------|--------------|-----------|
| Helm   | `helm.managed-by-label` | label `app.kubernetes.io/managed-by: Helm` (value case-insensitive) |
| Helm   | `helm.release-annotation` | annotation `meta.helm.sh/release-name` OR `meta.helm.sh/release-namespace` |
| Helm   | `helm.manager` | any managedFields manager == `helm` |
| Argo   | `argocd.tracking-id` | annotation `argocd.argoproj.io/tracking-id` present |
| Argo   | `argocd.instance-label` | label `analysis.argoInstanceLabelKey` (default `app.kubernetes.io/instance`) present — **see caveat** |
| Argo   | `argocd.manager` | any managedFields manager with prefix `argocd` (covers `argocd-controller`, `argocd-application-controller`) |
| Flux   | `flux.kustomize-label` | labels `kustomize.toolkit.fluxcd.io/name` AND `.../namespace` both present |
| Flux   | `flux.helm-label` | labels `helm.toolkit.fluxcd.io/name` AND `.../namespace` both present |
| Flux   | `flux.manager` | any managedFields manager in {`kustomize-controller`, `helm-controller`, `source-controller`} |
| Controller | `controller.owner-references` | `ownerReferences` non-empty |
| kubectl | `kubectl.manager` | any managedFields manager with prefix `kubectl` (covers `kubectl-client-side-apply`, `kubectl-create`, `kubectl-edit`, server-side-apply manager `kubectl`) |
| kubectl | `kubectl.last-applied` | annotation `kubectl.kubernetes.io/last-applied-configuration` present |
| — | `no-pipeline-markers` | no Helm/Argo/Flux family fired AND `ownerReferences` empty |

**Argo instance-label caveat.** Countless Helm charts set `app.kubernetes.io/instance`
with no Argo involvement. Therefore `argocd.instance-label` alone does NOT establish
`Owner == argocd`; Argo ownership requires `argocd.tracking-id` OR `argocd.manager` (the
instance label still appears in `Signals` as corroborating evidence and satisfies "has
pipeline markers" for the `no-pipeline-markers` check, since it at least indicates templated
deployment).

**Primary owner precedence** (first family with a qualifying signal wins):
`controller > flux > argocd > helm > kubectl > unknown`.
Rationale: ownerReferences means the parent is the interesting object regardless of other
markers; Flux/Argo commonly deploy Helm charts, so GitOps controllers outrank Helm
markers; Helm markers outrank kubectl evidence for the *primary* label because…

**…`KubectlWritten` is orthogonal.** `KubectlWritten = true` iff `kubectl.manager` fired,
regardless of primary owner. A manual `helm install` classifies as `Owner: helm,
KubectlWritten: true` — which is exactly the "annotations are not proof of pipeline
provenance" nuance, surfaced without corrupting the owner taxonomy. The table OWNER
column shows it as `helm+kubectl` (owner `kubectl` needs no suffix). It records the
kubectl *client*, not a person: a pipeline that runs `kubectl apply` sets it too.

`Managers` always carries the sorted, distinct managedFields manager names as raw
evidence for `--why` and the JSON report.

**Signal ordering** in `Signals`: table order above — stable for golden tests.

**System objects are never findings.** Before scoring, a resource the
control plane or cluster installer created itself is skipped — it is neither a
likely-manual finding nor a missing-from-cluster candidate, is never content-compared
(each cluster's `kube-root-ca.crt` holds its own CA, so copies always differ), and is
never a baseline entry. A resource is a system object when **no `kubectl*` manager wrote to it** and any
of these holds:
- every managedFields manager is an in-tree component: `kube-apiserver`,
  `kube-controller-manager`, `kube-scheduler`, `kubelet`, `kubeadm`,
  `cloud-controller-manager`, `clusterrole-aggregation-controller`,
  `service-cidr-controller`, `kubernetes-service-cidr-controller`, or a name starting
  `api-priority-and-fairness-config-`;
- it carries an API-server bootstrap marker: label
  `kubernetes.io/bootstrapping: rbac-defaults`, label
  `kube-aggregator.kubernetes.io/automanaged`, or annotation
  `apf.kubernetes.io/autoupdate-spec` (these cover clusters without managedFields);
- its identity is on the small built-in list (`default/kubernetes` Service; the
  `kube-root-ca.crt` ConfigMap and `default` ServiceAccount in any namespace).

Rationale: on a fresh cluster these objects carry no ownership markers, so without this
rule they would score as `no-pipeline-markers` — about 180 findings with `--include-cluster-scoped`,
and two per namespace (`kube-root-ca.crt`, the `default` ServiceAccount) once a namespace
is a month old. A `kubectl` write voids the rule because a
human editing a built-in object is worth reporting. Missing-from-cluster uses the same
test across all clusters, since built-ins legitimately differ between Kubernetes
versions. The lists live in `internal/analyze/system_ignore.go`.

## Consequences
- Deterministic, testable classification; the fixture set exercises every row.
- Multi-tool resources get one primary owner plus full evidence, so reports stay
  one-line-able.
- New families (e.g. Rancher Fleet, Carvel kapp) are additive rows here + new signal codes
  in interfaces.md via review.
