# Using kube-outlier

How to read a report, dig into a single finding, decide what to do about it, use
baselines, and gate a CI pipeline. For a hands-on tour against a local three-cluster
fleet, see [RUNNING.md](RUNNING.md).

## Reading the output

| Column | Meaning |
|---|---|
| `SCORE` | 0–100: how much evidence there is that the resource is a hand-made one-off; see [What the score means](#what-the-score-means). Findings below 40 are hidden by default (`--min-score`). `missing-from-cluster` findings always score 50; `content-differs` findings show `-`. |
| `KIND`, `NAMESPACE/NAME` | The resource. `KIND` is `group/Kind` (bare `Kind` for the core group), so the two columns paste straight into `--why`. Cluster-scoped resources show just the name. |
| `PRESENT-ON` | Only when 2+ clusters are scanned: how many of them have this resource. Against a baseline the column is called `IN-BASELINE` and counts the baseline's clusters, not the one being scanned. |
| `CLUSTER` | The clusters this finding is about: where the resource is, or for `missing-from-cluster`, where it is missing. A resource found on several clusters is one row listing all of them. A cluster's name is its kubeconfig context name. |
| `OWNER` | Who probably created it: `helm`, `argocd`, `flux`, `controller`, `kubectl`, or `unknown`. `helm+kubectl` (and so on) means it carries pipeline markers but a `kubectl` client also wrote to it. `-` means no single object was classified: `missing-from-cluster` and `content-differs` findings are about the fleet. |
| `AGE` | Days since creation (`-` when unknown). Older resources without an owner are more likely forgotten. |
| `WHY` | The evidence behind the score, as codes; see [What the score means](#what-the-score-means). `--why` shows the detail. |

The report goes to **stdout**. Warnings, skipped clusters and resource types, and a
summary line (`3 finding(s), 8 below the minimum score.`) go to **stderr**, so
`-o json > report.json` stays clean. In `-o markdown` the summary, and anything that was
skipped, are part of the report. On a terminal narrower than the table, each row's WHY
codes move to an indented `why:` line below it.

Output formats: `-o table|json|yaml|markdown`. `json` (and `yaml`, the same structure) is
the stable integration surface (`schemaVersion: 1`, see
[interfaces.md](interfaces.md#3-json-report-schema-schemaversion-1)); `markdown` pastes
into an issue or pull request.

### What the score means

The score adds up fixed points for the evidence found, capped to 0–100. It ranks
findings; it is not a probability. Findings below `--min-score` (default 40) are hidden.

| WHY code | Points | Fires when |
|---|---|---|
| `no-pipeline-markers` | +35 | No Helm, Argo CD or Flux labels or annotations, and no `ownerReferences` |
| `kubectl-manager` | +25 | `managedFields` shows a `kubectl` client wrote to it |
| `last-applied` | +10 | It carries `kubectl apply`'s `last-applied-configuration` annotation |
| `kind-weight` | +15 / +8 | RBAC objects, NetworkPolicies, Secrets and admission webhooks (+15); workloads, Services and Ingresses (+8) |
| `age` | +3 / +6 / +10 | Older than 7 / 30 / 90 days |
| `fleet-rare` | +20 | On at most `analysis.rareThreshold` of the clusters (default: 1 of 3) |
| `fleet-common` | −30 | On every cluster |
| `missing-from-cluster` | 50, fixed | On at least `analysis.commonThreshold` of the clusters, but not this one |
| `content-differs` | – | This cluster's copy differs from the majority version |

Is 60 bad? Not by itself: 60 is what a ConfigMap someone made with `kubectl create` this
week scores (35 + 25). Read the WHY codes, not the number. The weights are explained in
[ADR-0002](adr/0002-scoring-model-and-weights.md); the minimum score and fleet ratios are
tunable in [configuration](configuration.md).

`--why` and the JSON `ownership.signals` also list the evidence behind the codes, under
dotted names: `kubectl.manager` (the `managedFields` entry behind `kubectl-manager`),
`kubectl.last-applied`, `helm.release-annotation`, and so on.

## Understanding a finding

`--why` shows every piece of evidence behind a score — and if a resource *wasn't*
reported, it tells you why (below the minimum score, controller-owned, a system object,
or suppressed by a baseline exception). Its status names every finding the report shows
for the resource, including `missing-from-cluster` (even when the scanned cluster lacks
the resource) and, with `--compare-content`, `content-differs` with its field changes:

```console
$ kube-outlier scan --contexts c1,c2,c3 --why "ConfigMap team-a/debug-dump"
ConfigMap team-a/debug-dump

status: reported (score 100 >= minimum score 40)
presence: 1/3 clusters (ratio 0.33, source live)
  present on:   c1
  missing from: c2, c3

cluster c1:
  created: 2026-06-09T12:00:00Z (137d ago)
  owner: kubectl
  managers: kubectl-client-side-apply
  signals:
    kubectl.manager          manager=kubectl-client-side-apply
    kubectl.last-applied     kubectl.kubernetes.io/last-applied-configuration present
    no-pipeline-markers      no Helm, Argo CD or Flux markers, no ownerReferences

score:
  no-pipeline-markers  +35
  kubectl-manager      +25
  last-applied         +10
  age                  +10  (137d)
  fleet-rare           +20  (present on 1/3)
  total: 100 (base 80, fleet +20)
```

The identity format is `"<[group/]Kind> <[namespace/]name>"`, for example
`"apps/Deployment shop/api"` or `"rbac.authorization.k8s.io/ClusterRole admin"`. The
group is part of the kind: copy it from the table's `KIND` column, or from
`.resource.id` in the JSON.

If `--why` finds nothing, it exits `1` and says what it can tell:

```text
NetworkPolicy team-a/temp-allow-all not found in scope on any scanned cluster; did you mean "networking.k8s.io/NetworkPolicy team-a/temp-allow-all"?
ConfigMap kube-system/coredns not found in scope on any scanned cluster; namespace kube-system is excluded by default; scan it with -n kube-system
rbac.authorization.k8s.io/ClusterRole admin not found in scope on any scanned cluster; rbac.authorization.k8s.io/ClusterRole is cluster-scoped; add --include-cluster-scoped
```

## What to do with a finding

A finding is a prompt to look, not a verdict, and kube-outlier never changes anything.
Typical outcomes:

- **A forgotten one-off:** confirm with whoever owns the namespace, then delete it.
- **It should exist:** adopt it into your pipeline. Export it with
  `kubectl get <kind> <name> -n <namespace> -o yaml`, strip the server-filled fields (for
  example with `kubectl neat`), and commit it where the namespace's other resources live.
- **Legitimately hand-managed:** record it as a [baseline exception](#exceptions) with a
  reason and an `expires` date.
- **`missing-from-cluster`:** find out why the rollout skipped that cluster; `--why` lists
  where the resource is present.
- **`content-differs`:** the majority isn't necessarily right. Decide which version is
  intended, and fix it at the source.

## Baselines: the fleet presence signal without the fleet

Generate a baseline from a healthy fleet, commit it, and every later single-cluster
scan gets the fleet presence signal without touching the other clusters:

```sh
kube-outlier baseline generate --contexts c1,c2,c3 --out baseline.yaml
```

```sh
kube-outlier scan --contexts c1 --baseline baseline.yaml --fail-on inventory
```

The file holds presence counts and ownership tallies — **never manifests, content or
Secret values**. It does list your cluster (context) names and the kind, namespace and
name of every resource in scope, Secret names included, so commit it wherever you would
keep an inventory of the fleet. It doesn't record content, so the content comparison
needs live clusters. A baseline is a snapshot: regenerate it when the fleet changes (a
cluster added or removed, a large rollout).

It records the exact scan scope it was built with, and a scan with a different scope
stops rather than compare unlike with unlike: generate with the same `-n` and
`--include-cluster-scoped` flags, and the same config, that you scan with.

### Exceptions

Known-legitimate one-offs go in an `exceptions:` list at the top level of the baseline
file. `baseline generate` doesn't write one; add it yourself (regenerating onto the same
`--out` file keeps it, between `provenance` and `entries`):

```yaml
exceptions:
  - resource: networking.k8s.io/NetworkPolicy team-a/temp-allow-all
    reason: sanctioned during the Q3 migration, tracked in PLAT-1234
    approvedBy: platform-team
    expires: 2027-01-01
```

`resource` uses the same identity format as `--why`, group included. `reason` and
`approvedBy` are required. `expires` is optional: a date (midnight UTC) or an RFC 3339
time; leave it out for an exception that never expires. Matching findings are hidden and
counted in the summary (`1 suppressed by exception`, `summary.suppressedByException` in the
JSON). Expired exceptions are ignored and reported on stderr, and so is an exception that
names a resource neither the baseline nor the scanned cluster has, usually because of a
missing group (`did you mean "networking.k8s.io/NetworkPolicy team-a/temp-allow-all"?`).

**On a single cluster:** to silence a known resource without a fleet, generate a baseline
from that one cluster and add the exception there. A one-cluster baseline changes no
scores (`baseline generate` warns that it carries no rarity evidence), so it works as an
exceptions file. One difference from a plain scan: a resource deleted after you generate
it shows up as `missing-from-cluster` until you regenerate.

```sh
kube-outlier baseline generate --contexts prod-eu1 --out exceptions.yaml
kube-outlier scan --contexts prod-eu1 --baseline exceptions.yaml
```

## Other commands and flags

| Command / flag | What it does |
|---|---|
| `--compare-content` | Compare what shared resources contain across clusters, field by field, against the majority version. Needs 2+ reachable clusters. |
| `-n, --namespaces a,b` | Scan only these namespaces. Lists per namespace, so namespace-scoped access is enough. `--namespace` works too. A named namespace with nothing in it gets a warning (it probably doesn't exist). |
| `--include-cluster-scoped` | Also scan cluster-scoped kinds, built-in or custom (`ClusterRoleBinding`, CRDs, cluster-scoped custom resources, …). |
| `--min-score N` | Hide findings scoring below `N` (default 40). |
| `--contexts a,b` / `--all-contexts` | Which kubeconfig contexts to use (default: the current one). `--context` works too. |
| `--kubeconfig file` | Kubeconfig to load; repeat it to merge several, as `KUBECONFIG=a:b` would. A file named here must exist. |
| `--config file` | Config file to use instead of `./.kube-outlier.yaml` or `~/.config/kube-outlier/config.yaml`; `--config /dev/null` uses none. |
| `--request-timeout 30s` | Upper bound on each API request after the reachability check (`--probe-timeout`, default `5s`). |
| `-v` / `-vv` | Progress on stderr: steps and timings, or more detail. |
| `version` / `--version` | Print the installed version. |
| `completion zsh` | Print a shell completion script (also `bash`, `fish`, `powershell`); for example, add `source <(kube-outlier completion zsh)` to `~/.zshrc`. |

Run `kube-outlier <command> --help` for the full list and examples.

## Load on the API server

A scan runs API discovery once per cluster, then metadata-only `list` calls, 500 objects
per page, for each resource type in scope (per namespace with `-n`). It never watches and
never reads `spec` or `data`. Requests are rate-limited on the client to 25 a second, with
bursts of 50, per cluster, and up to four clusters are scanned at a time. So a cluster
with 50,000 objects across 60 types needs about 160 list requests: a few seconds at that
rate. `--compare-content` adds one `get` for each copy of each shared `content.kinds`
resource, eight at a time, which takes longer on clusters with thousands of ConfigMaps.
`-v` prints how long each cluster took.

## Using it in CI

```sh
kube-outlier scan --config /dev/null --baseline baseline.yaml --fail-on inventory -o json > findings.json
```

| Exit | Meaning |
|---|---|
| `0` | Clean run |
| `1` | Fatal — bad flags/config, unknown or duplicate context, zero reachable clusters, baseline scope mismatch, a content gate that cannot run, a `--why` resource not found |
| `2` | Partial — a cluster, resource type or (with `--compare-content`) object couldn't be read and was skipped, nothing else triggered (`0` with `--allow-partial`) |
| `3` | Inventory findings (`--fail-on inventory` or `any`): `likely-manual` or `missing-from-cluster` findings |
| `4` | Content differences (`--fail-on content` or `any`): `content-differs` findings |

Precedence when several apply: `1 > 3 > 4 > 2 > 0`. The JSON schema is frozen and
versioned — see [interfaces.md](interfaces.md#3-json-report-schema-schemaversion-1).

### Reading the JSON

Each finding has a `class` (`likely-manual`, `missing-from-cluster` or `content-differs`),
a `resource` whose `id` is the identity `--why` and baseline exceptions take, a `score`
with its `reasons`, and the `clusters` it is about. `partial` is `true` when anything was
skipped: an unreachable cluster, an entry in `skippedGVRs` (resource types, as
group/version/resource such as `v1/secrets`, that couldn't be listed), or an entry in
`skippedObjects` (copies the content comparison couldn't read).

Count findings by class, list them one per line, and fail a step on a partial run:

```sh
jq '.summary.byClass' findings.json
jq -r '.findings[] | [.score.total, .class, .resource.id, (.clusters | join(","))] | @tsv' findings.json
jq -e '.partial == false' findings.json
```

### A GitHub Actions job

A weekly scan of one cluster against a committed baseline. Give the job a kubeconfig for
a read-only identity (see [permissions](permissions.md)) however your pipeline provides
credentials; here it comes from a repository secret. Pin `@latest` to a release
(`@vX.Y.Z`) for reproducible runs.

```yaml
name: kube-outlier
on:
  schedule:
    - cron: "0 6 * * 1"
  workflow_dispatch: {}
permissions:
  contents: read
jobs:
  scan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: stable
      - run: go install github.com/vibe-shift-coding/kube-outlier/cmd/kube-outlier@latest
      - name: Scan
        env:
          KUBECONFIG_DATA: ${{ secrets.KUBECONFIG }}
        run: |
          printf '%s' "$KUBECONFIG_DATA" > "$RUNNER_TEMP/kubeconfig"
          code=0
          "$(go env GOPATH)/bin/kube-outlier" scan --kubeconfig "$RUNNER_TEMP/kubeconfig" \
            --config /dev/null --baseline baseline.yaml --fail-on inventory \
            -o json > findings.json || code=$?
          jq -r '.findings[] | "\(.score.total)\t\(.class)\t\(.resource.id)"' findings.json
          exit "$code"
```

### Things to know when gating a pipeline

- **Pass `--config` explicitly.** Without it, a `.kube-outlier.yaml` in the working
  directory is picked up automatically, and a file added to the repository could raise
  `minScore` or exclude namespaces. `--config /dev/null` reads no file.
- **Content is only compared with `--compare-content`** (or `content.enabled: true`),
  and only with 2+ reachable clusters (a baseline holds no content). `--fail-on content`
  when the comparison is off, cannot run, or has no `content.kinds` entry matching a
  resource type the scan listed is an error rather than a gate that can never fail;
  `--fail-on any` then gates on inventory findings only and says so on stderr. The
  comparison reads full objects, so it needs `get`: each copy it can't read is listed in
  `skippedObjects` and on stderr and makes the run partial, and if it can read none,
  `--fail-on content` exits `1`. System objects such as each namespace's
  `kube-root-ca.crt` are never compared: they differ per cluster by design.
- **The exit code reports the highest-priority trigger only.** With `--fail-on any`, a run
  with both inventory findings and content differences exits `3`; a run with inventory
  findings that also skipped a cluster exits `3`, not `2`. The JSON report has the full
  picture: `summary.byClass` counts each finding class, and `partial` marks a partial run.
- **Partial runs exit `2` by default,** so a single forbidden type, an API group that
  fails discovery, or an unreachable cluster fails a CI step. (The aggregated metrics
  APIs are exempt: their types are never scanned.) If that is expected in your
  environment, pass `--allow-partial` (or set `report.allowPartial: true`): skips are
  still reported on stderr and in the JSON, but no longer change the exit code.
- **A typo in the config fails the run.** An unknown key in the config file, or an
  unknown `KUBE_OUTLIER_*` variable, exits `1` instead of silently using a default. A
  kind or field path that matches nothing is reported on stderr (see
  [configuration](configuration.md)).
- `--fail-on` has no effect with `--why`.
