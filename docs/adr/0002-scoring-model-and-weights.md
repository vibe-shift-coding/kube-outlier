# ADR-0002: Ownership + fleet scoring model and weights

## Context
The score has two steps (Tier 1 base + Tier 2 fleet modifier) and findings are suppressed
below a reporting threshold. Without a fixed scale, threshold and weights, scores would be
arbitrary and impossible to test. All numbers below are **defensible starting points,
explicitly tunable**; they live in `internal/analyze/weights.go` (one constants file with
rationale comments) and only there.

## Decision

**Scale.** Score is an integer. `Total = clamp(Base + FleetModifier, 0, 100)`. Findings
with `Total < analysis.minScore` (default **40**) are suppressed (counted in
`summary.suppressedBelowMinScore`).

**Tier 1 base signals (additive):**

| reason code      | points | fires when |
|------------------|-------:|------------|
| `no-pipeline-markers` | +35 | no Helm/Argo/Flux markers AND no ownerReferences |
| `kubectl-manager`| +25 | any managedFields manager with prefix `kubectl` (ADR-0005) |
| `last-applied`   | +10 | `kubectl.kubernetes.io/last-applied-configuration` annotation present |
| `kind-weight`    | +15 | Kind in HIGH set (detail `<Kind>: high`): Role, RoleBinding, ClusterRole, ClusterRoleBinding, NetworkPolicy, Secret, ValidatingWebhookConfiguration, MutatingWebhookConfiguration |
| `kind-weight`    | +8  | Kind in MEDIUM set (detail `<Kind>: medium`): Deployment, StatefulSet, DaemonSet, CronJob, Job, Service, Ingress |
| `age`            | 0 / +3 / +6 / +10 | age < 7d / 7–30d / 30–90d / > 90d (age = now − creationTimestamp) |

Resources with non-empty `ownerReferences` are excluded before scoring — they never
become findings; the parent is the interesting object. There is no setting to score them
anyway: even a `kubectl create job --from=cronjob` Job scores 33, below the minimum
(ADR-0010).

**Tier 2 fleet modifiers (exactly one of the first two may apply):**

| reason code        | points | fires when |
|--------------------|-------:|------------|
| `fleet-rare`       | +20 | presence ratio `r <= rareThreshold` (default 0.34) |
| `fleet-common`     | −30 | `r == 1.0` |
| (partial band)     |   0 | otherwise |

`r in [commonThreshold, 1.0)` (default 0.6) additionally emits a separate `missing-from-cluster`
finding per absent cluster (fixed score 50, reasons `[]`, class-specific message); it does
not modify the resource's own score. Emission is controlled by
`analysis.reportMissingFromCluster` (default true).

**Minimum score applies post-modifier.** When the fleet presence signal is active, the
`minScore` filter compares against `Total` (Base + FleetModifier, clamped), not
`Base`. This is what the sanity checks below already assume: `fleet-rare` can promote a
base-35 finding to 55 (reported), and `fleet-common` can demote a base-60 finding to
30 (suppressed). The fixed-score missing-from-cluster class is compared
against `minScore` like any other finding; at the default 40 its fixed 50
always clears it.

**Small-N semantics.** Buckets are purely ratio-driven; there is no minimum-cluster setting. At
`N == 2` the only reachable modifier is `fleet-common` (r ∈ {0.5, 1.0}; 0.5 is the
partial band) — two clusters are too little evidence to *raise* suspicion, but
present-everywhere is still meaningful demotion evidence.

## Consequences
Sanity checks the implementation must reproduce (also encoded as unit tests):
- Hand-applied 137-day-old ConfigMap: 35+25+10+0+10 = **80** → reported.
- `kubectl create`d 6-day-old NetworkPolicy: 35+25+0+15+0 = **75** → reported.
- Helm-managed Deployment, any age: 0+0+0+8+≤10 ≤ **18** → suppressed in Tier 1.
- Helm-marked but rare (Tier 2): base 0–25 (kind + age only) + 20 = 20–45 → a medium- or
  high-weight kind that has been around for a while surfaces near the threshold at low
  priority, matching the design rule "ownership lowers suspicion, it doesn't exempt".
- Un-annotated-but-ubiquitous with no kubectl evidence: 35+15+10 = 60 − 30 = **30** →
  demoted below threshold. By design, if it *also* carries strong kubectl evidence
  everywhere (95 − 30 = 65) it stays visible — someone hand-applied the same thing
  fleet-wide, which is worth seeing.
- Max base = 35+25+10+15+10 = 95; nothing reaches 100 without the fleet presence signal.

Tuning changes touch `weights.go` + this ADR only; changing reason codes or the Score
shape requires an interfaces.md review.
