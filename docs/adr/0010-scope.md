# ADR-0010: Scope — what kube-outlier does not do

## Context
kube-outlier's job is finding forgotten hand-made one-offs, with the ownership and fleet
presence signals; the content comparison is an optional add-on. Every flag, setting,
JSON field and finding class is part of the frozen contract (interfaces.md), so removing
one is a breaking change while adding one is additive. Each feature has to justify its
contract surface, its Secret-handling surface, and the tests and docs it needs.

## Decision
kube-outlier does not provide the features below. Each could be added later as an
additive change, once there is demand and the problem noted is solved.

| Not provided | Why | What adding it would need |
|---|---|---|
| Manifest export (writing reported resources as normalized YAML) | It is remediation, not finding: the usual action for a forgotten one-off is to delete it, and adopting one is served by `kubectl get -o yaml` plus `kubectl neat`. It would write the full body of every reported resource to disk, the largest Secret-handling surface the tool could have. | `sensitive.kinds` (ADR-0009) applied to every value written, and a non-zero exit when nothing could be read. |
| Content hashes in baselines (comparing one cluster's content against a baseline) | A hash shows only *that* content differs, never which field, and it is valid only under the normalization rules (ADR-0006), `content.ignoreFields` and `sensitive.kinds` that produced it, none of which a baseline records. | Those inputs recorded in the baseline and checked on use, so a mismatch fails instead of reporting every entry as different. |
| Saved scan results for offline re-analysis | A metadata scan takes seconds, so offline re-analysis mostly serves maintainers, and it would be a second portable file format beside baselines, with its own scope check and Secret rules. | A frozen file schema in interfaces.md and the same scope check baselines use. |
| A finding for an API type served on only part of the fleet | It would fire for empty types, for built-in kinds a newer Kubernetes version adds during a staged upgrade, and falsely when an API group fails discovery. With `--include-cluster-scoped`, a CRD missing from a cluster is already a `missing-from-cluster` finding. | A rule that reports only types whose objects exist on other clusters. |
| Leaving cluster names out of baselines | There is no user story where cluster names are secret but the baseline is committed, and the names make a baseline auditable. | A concrete user story. |
| Filters that hide findings before the exit code is computed (rare-only, pipeline-owned) | A filter applied there makes a CI gate pass silently; a pipeline-owned filter would also hide `helm+kubectl` findings, the case the OWNER column exists to show. The same views are a `jq` filter on the JSON report. | Filtering applied to the rendered report only, never to `--fail-on`. |
| Scoring resources that have `ownerReferences` | Controller-owned resources are always skipped: the owner is the interesting object, and even the best case, a Job from `kubectl create job --from=cronjob`, scores 33, below the minimum score. | Evidence that such resources are forgotten one-offs in practice. |

The `completion` command cobra generates is available but hidden from help.

## Consequences
The contract stays small: three finding classes, no export package, no content hashes
in baselines, and no export or saved-result files to keep Secret-free. The content
comparison works on live clusters only, so it needs two or more reachable clusters.
ADR-0008 records the names chosen for these features, so an addition reuses them.
