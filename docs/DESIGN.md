# kube-outlier — design

> **Contract precedence.** Type shapes, package import rules, the JSON schema, and exit
> codes are frozen in [`interfaces.md`](interfaces.md). Settled decisions (diff library,
> score weights, config schema, baseline format, ownership rules, content-comparison
> canonicalization, naming, sensitive kinds) live in [`adr/`](adr/). Where this document's
> prose and those files differ, **those files win**.

## What it is

`kube-outlier` is a read-only CLI for platform teams that run long-lived Kubernetes
clusters where nearly everything is deployed by pipelines (Helm, Argo CD, Flux). It
surfaces resources that were probably created by hand as one-offs — a `kubectl apply`
during an incident, a debug ConfigMap, a test CronJob — and never cleaned up.

## Layered signal model

Each layer works standalone. Supplying more clusters strengthens the results but is
never required.

**Tier 1 — Ownership scan (single cluster, always on).**
On any one cluster, flag resources that lack pipeline-ownership markers (Helm/Argo/Flux
labels and annotations), have no `ownerReferences`, and carry `managedFields` evidence of
human `kubectl` authorship. High precision, needs nothing but one context. Conceptually
similar to Argo CD's orphaned-resources feature, without the Argo dependency and with
richer "a human did this" evidence from `managedFields`.

**Tier 2 — Fleet enrichment (two or more clusters, or a baseline).**
Cross-cluster presence *adjusts* Tier 1 scores rather than replacing them:

- Un-annotated **and** absent from most clusters → suspicion raised (strong "forgotten
  here" signal).
- Un-annotated but present on **every** cluster → suspicion lowered (probably a
  legitimate un-labelled baseline resource).
- Annotated (looks pipeline-managed) but present on only one cluster → suspicion raised
  too, but from a low base: it is reported only when kind weight and age push it over
  the minimum score (a stray `helm install`, or an Argo app targeting a single cluster),
  and `--why` always shows it.
- Present on most clusters but **missing from one** → reported as a distinct anomaly
  class — often the more dangerous case, e.g. a NetworkPolicy that never reached one
  cluster.

**Tier 3 — Content comparison (opt-in; two or more reachable clusters).**
For resources with the same identity (group + kind + namespace + name, version-agnostic)
present on most clusters, a normalized field-by-field diff shows where one cluster's copy
differs from the majority version. A lagging image tag is the headline use case.

User-facing docs call Tiers 1 and 2 the *ownership* and *fleet presence* signals and Tier 3
the *content comparison* ([ADR-0007](adr/0007-user-facing-naming.md)); the tier numbers
are kept only in this document.

### Why layered, not "the fleet is the source of truth"

Ownership filtering is the stronger foundation: it works everywhere (including
single-cluster shops), is easy to reason about, and is precise. Fleet consensus is a
valuable *enrichment* but a weak *foundation*:

1. **Ownership markers are spoofable.** A manual `helm install` sets the same
   `managed-by: Helm` annotations as a pipeline; a hand-applied manifest can carry Argo
   labels. Annotations are never treated as proof of pipeline provenance — they are
   combined with the `managedFields` manager and, when available, fleet presence.
2. **Consensus assumes the majority is right.** If a bad change rolled out to most
   clusters, naive consensus flags the correct minority. The tool therefore reports
   "here is what is unusual — you decide", never "here is what is wrong", and keeps report
   wording neutral.
3. **Single-cluster is never a degraded experience.** Tier 1 is fully useful with one
   context. Fleet features layer on; they don't gate. There is no minimum cluster count.

### Hard rules

- **Strictly read-only.** The tool never mutates cluster state — no create, update,
  patch, apply, or delete. Remediation is left to a human; the report says what is
  unusual and why. CI enforces this (`scripts/check-readonly.sh`).
- **Secrets.** Secret `data`/`stringData` values are never printed. Reports carry
  presence and metadata only; content comparison compares Secret values as hashes and
  reports them only as digests keyed per run; the last-applied-configuration
  annotation's value is never kept.
- **Fail soft per cluster.** An unreachable cluster, a forbidden resource type or an
  object the content comparison cannot read is reported and skipped, never fatal, and
  never treated as absent or identical; the run exits `2` (partial).

## Technology

- **Go**, single static binary.
- **k8s.io/client-go** — kubeconfig/context loading (`clientcmd`), discovery, the
  metadata client for cheap listing, the dynamic client for targeted full-object reads.
- **k8s.io/apimachinery** — `unstructured`, GVK/GVR mapping, `resource.Quantity`.
- **spf13/cobra** + **knadh/koanf** — CLI and layered config: flags > env > file
  ([ADR-0003](adr/0003-config-loading-and-schema.md)).
- **sigs.k8s.io/yaml** — YAML in/out with JSON semantics.
- **wI2L/jsondiff** — RFC 6902 diffs of normalized objects, rendered by our own report
  layer ([ADR-0001](adr/0001-structural-diff-via-jsondiff.md)). `google/go-cmp` is
  test-only.
- **golang.org/x/sync/errgroup** — bounded per-cluster and per-GVR concurrency.
- **log/slog** — structured logs to stderr; stdout carries only the report.

## Repository layout

```
cmd/kube-outlier/   cobra entrypoint and subcommands (clusters, scan, baseline, version)
internal/
  config/           config schema, loading, validation, built-in defaults
  connect/          kubeconfig/context resolution, client construction, health probe
  discovery/        per-cluster API resource enumeration and scope filtering
  inventory/        metadata-only listing (Pass A), targeted fetch (Pass B)
  identity/         resource identity model and fleet presence matrix
  ownership/        Helm/Argo/Flux/controller/kubectl classification
  analyze/          scoring, fleet enrichment, baseline comparison, content comparison
  baseline/         baseline artifact types, generation, loading, scope validation
  normalize/        field stripping and canonicalization for the content comparison
  report/           table/json/yaml/markdown renderers, --why view, exit codes
  orderedyaml/      YAML in JSON field order, for reports and baselines
  testutil/         shared fixture loading for tests
scripts/            read-only guard, local 3-cluster fleet up/down/smoke
testdata/           fixture manifests, golden reports, baseline and normalize fixtures
docs/               this document, interfaces.md, ADRs, RUNNING.md, SETUP.md
```

## Core concepts

### 1. Cluster set and connectivity

- Input: `--contexts a,b,c`, `--all-contexts`, repeated `--kubeconfig`, or the config
  file's `clusters` section. Each cluster's display name is its context name.
- Every cluster is probed (`/version`) with a short timeout (`--probe-timeout`, default
  5s). Unreachable clusters are warned about, marked skipped, and excluded. The
  *effective fleet size* `N` is the number of reachable clusters; all ratios use it.
- Tier 1 runs with one cluster. Fleet enrichment activates automatically at `N >= 2`;
  content comparison also needs `N >= 2`.
- Client-side rate limiting defaults to QPS 25 / burst 50 to keep the load on busy API
  servers low. Every request after the probe is bounded by `--request-timeout` (default
  30s), and the client transport refuses any HTTP method other than GET and HEAD.

### 2. Resource discovery

- Per cluster, enumerate served resource types whose verbs include `list`.
- Default scope is namespaced resources; cluster-scoped kinds (`ClusterRole`,
  `ClusterRoleBinding`, CRDs, …) are opt-in via `--include-cluster-scoped` because they
  are valuable but noisier.
- Presence comparison only counts clusters that actually serve (and could list) a
  type, so a type served on only some clusters (a CRD installed on one) never makes its
  resources look missing elsewhere. The type itself is not a finding (ADR-0010).
- An API group whose discovery fails (an aggregated API that is down) is reported as a
  skipped type and makes the run partial; the aggregated metrics APIs are exempt
  ([ADR-0003](adr/0003-config-loading-and-schema.md)).
- A built-in exclusion list removes high-noise kinds (`Event`, `EndpointSlice`,
  `Endpoints`, `Lease`, `Pod`, `Node`, `ReplicaSet`, `ControllerRevision`,
  `ComponentStatus`, metrics APIs) and system namespaces. User config extends it; it is
  never silently overridden.
- **System objects are never findings** (of any class: they are not content-compared
  either): anything the control plane or installer created
  itself — default RBAC, built-in APIServices, API priority-and-fairness objects, system
  PriorityClasses and Namespaces, each namespace's `kube-root-ca.crt` and `default`
  ServiceAccount. They are recognised by their managedFields managers (only in-tree
  components such as `kube-apiserver` or `kubeadm`) or the API server's bootstrap
  markers, and the rule never applies once a `kubectl` client has written to the object
  ([ADR-0005](adr/0005-ownership-classification-rules.md)).

### 3. Inventory

- **Pass A (presence):** metadata-only lists (`PartialObjectMetadata` via the metadata
  client) of everything in scope on every cluster. Paginated (500 per page), bounded
  concurrency per cluster and per GVR. Cheap even on large clusters.
- **Namespace allowlist:** by default each type is listed cluster-wide. With
  `--namespaces a,b` (`scope.namespaces.include`), namespaced types are listed once per
  named namespace instead, so read access granted by a namespace `RoleBinding` is
  enough. Cluster-scoped types are still listed cluster-wide (and only when
  `--include-cluster-scoped` is set).
- **Pass B (bodies):** targeted `Get`s — never lists — only for the `content.kinds`
  resources the content comparison compares.
- The last-applied-configuration annotation's value is emptied at collection, since it
  can embed a Secret's data; its presence is kept as evidence.

### 3b. Baseline artifact (portable fleet fingerprint)

A committable summary of what "normal" looks like across the fleet, so a later run can
compare a *single* cluster against it without re-scanning peers. Useful for CI checks,
air-gapped or single-cluster environments, and an approvable known-good state.

- `kube-outlier baseline generate --contexts … --out baseline.yaml` produces it;
  `kube-outlier scan --baseline baseline.yaml --contexts one` consumes it.
- **Fingerprint only — never manifests, content or secret values:** per identity, the
  presence ratio, cluster count and an ownership tally.
- **Provenance header:** schema version, tool version, generation time, contributing
  clusters, and the exact scope
  used. A scan refuses a baseline whose scope differs from its own, unless
  `--allow-scope-mismatch` is given, because mismatched exclusions silently corrupt
  comparisons.
- **Approved exceptions:** a hand-maintained list of identities known to be legitimate
  one-offs, suppressed from future reports and carried over on regeneration.

Format and validation rules: [ADR-0004](adr/0004-baseline-artifact-format.md).

### 4. Identity and presence matrix (Tier 2)

- Identity = `(group, kind, namespace, name)`; namespaces are assumed to be named
  identically across clusters.
- The presence matrix maps identity → clusters where present; presence ratio
  `r = |clusters with it| / N` (per-identity denominators exclude clusters that couldn't
  list the type).
- Buckets *adjust* the Tier 1 score; they are not verdicts on their own:
  - `r == 1.0` → common: lowers suspicion for un-annotated resources; content-comparison candidate.
  - `commonThreshold <= r < 1.0` (default 0.6, so 2 of 3) → common with gaps: each absent
    cluster gets a `missing-from-cluster` finding.
  - `r <= rareThreshold` (default 0.34) → rare: raises the score.
  - in between → partial: no modifier (often intentional per-region variance).

### 5. Ownership classification (Tier 1 — the core signal)

For every in-scope resource, classify how it was likely created, from metadata alone
([ADR-0005](adr/0005-ownership-classification-rules.md)):

- **Helm:** `app.kubernetes.io/managed-by: Helm`, `meta.helm.sh/release-*` annotations.
- **Argo CD:** the instance label (key configurable — `application.instanceLabelKey` can
  be customized) and/or `argocd.argoproj.io/tracking-id`.
- **Flux:** `kustomize.toolkit.fluxcd.io/*`, `helm.toolkit.fluxcd.io/*` labels.
- **Controller-owned:** non-empty `ownerReferences` — excluded by default (the parent is
  the interesting object).
- **kubectl:** the `last-applied-configuration` annotation and/or `managedFields`
  managers such as `kubectl-client-side-apply`, `kubectl-create`, `kubectl-edit`. The
  `managedFields` manager is the strongest "a human did this" signal — with the caveat
  that it identifies the client, not the person, so a pipeline that runs `kubectl apply`
  looks the same; markers and fleet presence separate the two.
- **Unknown:** none of the above.

Primary-owner precedence when several markers match: controller > flux > argocd > helm >
kubectl > unknown. `KubectlWritten` is an orthogonal boolean, so a Helm-labelled object
last written by `kubectl` is still flagged as kubectl-written (`helm+kubectl` in the table).

**Scoring** ([ADR-0002](adr/0002-scoring-model-and-weights.md); weights live only in
`internal/analyze/weights.go`):

- **Base score (always):** no pipeline markers, `kubectl` manager, last-applied
  annotation, kind weight (RBAC bindings, NetworkPolicies and Secrets weigh more than
  ConfigMaps), and age.
- **Fleet modifier (Tier 2 only):** rare raises the score, present-everywhere lowers it.
- Score is an integer clamped to 0–100; findings below `analysis.minScore`
  (default 40) are suppressed. The report always shows *which signals fired* and whether
  fleet enrichment was applied — never just a number.

Ownership lowers the score; it does not exempt.

### 6. Configuration

YAML config (`.kube-outlier.yaml`, or `$XDG_CONFIG_HOME/kube-outlier/config.yaml`), all
sections optional. Kinds use one canonical syntax everywhere: `group/Kind`, with the core
group as bare `Kind` (`Event`, `apps/Deployment`, `networking.k8s.io/Ingress`).

```yaml
clusters:
  contexts: [cluster-eu1, cluster-eu2, cluster-us1, cluster-us2]
  # or kubeconfigs: [~/.kube/eu.yaml, ~/.kube/us.yaml]   (merged as KUBECONFIG=a:b)

scope:
  includeClusterScoped: false
  namespaces:
    include: []                                 # if set: scan only these (overrides exclusions)
    exclude: [velero, monitoring-scratch]       # added to the built-in system namespaces
    excludePatterns: ["ephemeral-.*", "pr-\\d+"]
  kinds:
    exclude: ["batch/CronJob"]                  # added to the built-in noisy kinds
    include: []                                 # if set, allowlist mode

analysis:
  commonThreshold: 0.6
  rareThreshold: 0.34
  minScore: 40
  argoInstanceLabelKey: app.kubernetes.io/instance
  reportMissingFromCluster: true

content:
  enabled: false
  kinds: [apps/Deployment, ConfigMap, networking.k8s.io/Ingress]
  ignoreFields:                                 # extra paths stripped before comparison
    - spec.replicas                             # e.g. HPA-managed
    - metadata.annotations["deployment.kubernetes.io/revision"]

report:
  format: table      # table | json | yaml | markdown
  failOn: none       # none | inventory | content | any
  allowPartial: false # true: a partial run exits 0 instead of 2
```

Every setting can also come from the environment: `KUBE_OUTLIER_` plus the path with
`__` between sections, case-insensitive (`KUBE_OUTLIER_ANALYSIS__MINSCORE=60`,
`KUBE_OUTLIER_SCOPE__NAMESPACES__INCLUDE=team-a,team-b`). Precedence: flags > env >
file > built-in defaults.

Full schema and validation rules: [ADR-0003](adr/0003-config-loading-and-schema.md).

### 7. Normalization

Used by the content comparison
([ADR-0006](adr/0006-content-canonicalization-and-majority.md)), in two layers: a
conservative base and comparison-only strips on top.

- **Always dropped:** `status`, `metadata.managedFields`, `resourceVersion`, `uid`,
  `generation`, `creationTimestamp`, `selfLink`, the last-applied annotation, revision
  annotations, Argo/Flux/Helm bookkeeping annotations, and per-kind server assignments
  such as Service `clusterIP`/`nodePort` and PVC `volumeName`.
- **Server-defaulted noise** (comparison-only strips): pod-template fields such as
  `terminationMessagePath`, `dnsPolicy`, `schedulerName`. These lists are data in an
  embedded `defaults.yaml`, not Go code; users add paths via `content.ignoreFields`.
- **Canonicalization:** map keys sorted, array order preserved (env-var order can
  matter), resource quantities compared by value (`100m` == `0.1`), empty map/slice equal
  to absent.
- **Secrets:** hashed (`sha256:…`) for content comparison and re-keyed per run
  (`hmac-sha256:…`) before reporting. Kinds in `sensitive.kinds`
  get the same treatment for every value outside `apiVersion`, `kind` and `metadata`
  (ADR-0009).

### 8. Content comparison

The content comparison looks at copies of one resource differing between
clusters. It is distinct from GitOps drift (live state versus Git), which this tool does
not measure — see the last point below.

- For each identity present on at least `commonThreshold` of clusters, matching
  `content.kinds` and not a system object: normalize every copy, group copies by canonical hash, and take the
  largest group as the **majority version** (ties, and a largest group that holds no
  more than half the copies, are reported as ambiguous: "most common version"). The finding
  class is `content-differs`.
- Diff each minority copy against the majority version as JSON Patch ops, rendered
  `path: majorityValue → clusterValue`.
- Summarize the fields that differ most across the fleet ("`spec.template.spec.containers[0].image`
  differs on 3 cluster(s)").
- It compares live clusters only, so it needs two or more reachable clusters; a baseline
  stores no content (ADR-0010).
- No git or desired-state comparison — that is Argo CD's and Flux's job. The majority
  version *is* the desired-state proxy here.

### 9. Reporting

- Default: a human table sorted by score, one line per finding:
  `SCORE  KIND  NAMESPACE/NAME  [PRESENT-ON]  CLUSTER  OWNER  AGE  WHY`. On a terminal
  narrower than the table, WHY moves to an indented line under each row.
- `-o json|yaml|markdown` with a versioned schema (`schemaVersion: 1`).
- Exit codes: 0 clean, 1 fatal, 2 partial run (something skipped), 3 inventory findings,
  4 content differences; precedence `1 > 3 > 4 > 2 > 0`. `report.failOn` / `--fail-on` decides
  whether findings affect the exit code; failing on content requires the content comparison
  to be enabled.
  `report.allowPartial` / `--allow-partial` turns a partial run's 2 into 0.
- `--why "Kind ns/name"` deep-dives one identity: presence map, ownership evidence,
  `managedFields` managers, and per-cluster creation time — including for resources that
  were *not* flagged. Its status names every finding the report shows for the identity
  (missing-from-cluster and content-differs included), so the two never disagree.

## RBAC

Read-only access: `get` and `list` on the resource types in scope, plus discovery —
cluster-wide by default, or only inside the named namespaces with `--namespaces`.
Anything the identity can't list is reported as `forbidden, skipped` (per namespace when
listing per namespace) and excluded from presence denominators; the run exits `2`.
Without `get`, the content comparison reports each copy it couldn't read the same way;
if it could read none, `--fail-on content` exits `1`.

## Testing

- **Unit:** identity and presence math, scoring, classification, normalization
  (table-driven with fixture YAML), config validation.
- **Fake clients:** pagination and forbidden-GVR degradation.
- **Golden files:** every report format and pipeline (single-cluster, fleet, baseline,
  content comparison, `--why`) is byte-compared against `testdata/golden/`.
- **kind e2e (CI):** one kind cluster with two hand-applied resources and a
  Helm-labelled control; the scan must flag exactly the two manual ones.
- **Local fleet smoke test:** `scripts/fleet-up.sh` + `scripts/fleet-smoke.sh` exercise
  the fleet presence signal, baselines, `--why`, namespace-only access and the content
  comparison against three real clusters.

## Engineering conventions

- Small, reviewable commits; Conventional Commits.
- Every exported identifier is documented. Every heuristic (score weights, default
  ignore lists) lives in a data file or a single constants file with rationale comments.
- No mutation-capable client calls anywhere — enforced by `scripts/check-readonly.sh`.
- Context propagation and timeouts on every API call; no `context.TODO()` outside tests.
- New dependencies must be maintained and widely used; keep the module graph small.

## Non-goals

- No auto-remediation or deletion, no admission control, no in-cluster agent or
  operator — a pure CLI talking to API servers.
- No git or Helm-chart rendering comparison.
- No fuzzy or "similar but renamed" matching.
- No web UI — JSON output is the integration surface.
