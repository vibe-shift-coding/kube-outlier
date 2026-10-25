# Configuration

Everything works with zero config. To tune it, drop a `.kube-outlier.yaml` in the working
directory, or `$XDG_CONFIG_HOME/kube-outlier/config.yaml` (`~/.config/kube-outlier/config.yaml`
when `XDG_CONFIG_HOME` is unset), or point at a file with `--config`. When a file is
picked up from one of the default locations, the tool names it on stderr
(`using config file .kube-outlier.yaml`), so a file left in a working directory never
changes a scan silently. `--config /dev/null` reads no file at all: use it in CI, where
the working directory may hold a file you didn't write.

Precedence is **flags > environment > file > defaults**. Environment variables use
`KUBE_OUTLIER_` with `__` between sections and are case-insensitive, for example
`KUBE_OUTLIER_REPORT__FAILON=inventory` or
`KUBE_OUTLIER_SCOPE__NAMESPACES__INCLUDE=team-a,team-b`. List values are comma-separated.

Loading is strict: a key in the file, or a `KUBE_OUTLIER_*` variable, that names no
setting stops the run with exit code `1` and names it. A typo never falls back to a
default.

## Example

All sections are optional. The comment on each line says whether its value is the default
or an example:

```yaml
clusters:
  contexts: [cluster-eu1, cluster-us1]  # example; default: the current context
  allContexts: false                    # default; true = every context (--all-contexts)
  kubeconfigs: []                       # default: as kubectl ($KUBECONFIG, else ~/.kube/config)
  probeTimeout: 5s                      # default; the reachability check, per cluster
  requestTimeout: 30s                   # default; every other API request

scope:
  includeClusterScoped: false           # default; ClusterRoleBindings etc. are noisier, so opt-in
  namespaces:
    include: []                         # default; if set, scan only these (same as -n)
    exclude: [velero, monitoring-scratch]         # example; added to the built-in exclusions
    excludePatterns: ["ephemeral-.*", "pr-\\d+"]  # example; default []
  kinds:
    exclude: [batch/Job]                # example; added to the built-in exclusions
    include: []                         # default; if set, scan only these kinds

analysis:
  minScore: 40                          # default; findings scoring below this are hidden
  commonThreshold: 0.6                  # default; see the table below
  rareThreshold: 0.34                   # default; see the table below
  reportMissingFromCluster: true        # default
  argoInstanceLabelKey: app.kubernetes.io/instance  # default; see the table below

content:
  enabled: false                        # default; true = always --compare-content
  kinds: [apps/Deployment, ConfigMap, networking.k8s.io/Ingress]  # default
  ignoreFields:                         # default []; both entries are examples
    - spec.replicas                     # not ignored by default; add it if an HPA scales your Deployments
    - metadata.annotations["example.com/deployed-at"]

sensitive:
  kinds: []                             # default; extra kinds handled like Secrets (Secret always is)

report:
  format: table                         # default; table | json | yaml | markdown
  failOn: none                          # default; none | inventory | content | any
  allowPartial: false                   # default; true: skipped clusters/types don't exit 2
```

| Setting | Flag | Meaning |
|---|---|---|
| `analysis.minScore` | `--min-score` | Findings scoring below this (0–100) are hidden and counted in `summary.suppressedBelowMinScore`. |
| `analysis.commonThreshold` | — | A resource on at least this share of clusters is *common*: each cluster missing it gets a `missing-from-cluster` finding. The default 0.6 means 2 of 3, 3 of 4 or 3 of 5. It does not decide `fleet-common` (−30), which needs the resource on every cluster. |
| `analysis.rareThreshold` | — | A resource on at most this share of clusters is *rare* and gets `fleet-rare` (+20). Must be below `commonThreshold`. |
| `analysis.reportMissingFromCluster` | — | `false` turns off `missing-from-cluster` findings. |
| `analysis.argoInstanceLabelKey` | — | The Argo CD instance label, if you changed Argo's `application.instanceLabelKey` (often to `argocd.argoproj.io/instance`). |
| `clusters.kubeconfigs` | `--kubeconfig` | Kubeconfig files to merge, as `KUBECONFIG=a:b` would. Each must exist; a leading `~/` means your home directory. |
| `scope.kinds.include` | — | If set, only these kinds are scanned. The built-in exclusions still apply. |
| `content.enabled` | `--compare-content` | Compare what shared resources contain across clusters. |
| `content.kinds` | — | The kinds the content comparison reads in full and compares. |
| `content.ignoreFields` | — | Fields left out of the content comparison; see [Ignoring fields](#ignoring-fields-in-the-content-comparison). |
| `sensitive.kinds` | — | Extra kinds whose values are handled like a Secret's; see [Sensitive kinds](#sensitive-kinds). |

Kinds use one syntax everywhere: `group/Kind`, with the core group written as a bare
`Kind` (`Event`, `apps/Deployment`, `networking.k8s.io/Ingress`). The group is part of the
name: `Deployment` alone means a core-group kind of that name, which doesn't exist. An
entry in `scope.kinds`, `content.kinds` or `sensitive.kinds` that matches no resource type
on any scanned cluster is reported on stderr with the spelling it probably meant
(`did you mean "apps/Deployment"?`). If no `content.kinds` entry matches, `--fail-on content`
exits `1` rather than pass.

## Ignoring fields in the content comparison

`content.ignoreFields` paths are dot-separated keys, with `[*]` for every element of a
list, `[N]` for one element, and `["…"]` for a key that contains dots. Each path applies to
every kind in `content.kinds`:

```yaml
content:
  ignoreFields:
    - spec.replicas
    - spec.template.spec.containers[*].image
    - spec.template.spec.containers[0].env
    - metadata.annotations["example.com/deployed-at"]
```

The paths the report prints work as they are: copy `spec.template.spec.containers[0].image`
from the table output, or `/spec/template/spec/containers/0/image` from the JSON's `path`.
A path that matches no field in any compared copy is reported on stderr
(`content.ignoreFields: "…" matched no field in any compared copy`); that is usually a typo.

Server-filled defaults such as `terminationMessagePath`, `dnsPolicy` and `schedulerName`
are already ignored. `spec.replicas` is not, so add it if an autoscaler scales your
Deployments.

## Sensitive kinds

A core `Secret` is always handled safely: its values are compared as hashes and
reported only as per-run digests. Custom
resources can hold credentials too (for example the external-secrets `Fake` generator,
or an operator resource with an inline password). List them in `sensitive.kinds` to give
them the same treatment for every value outside `apiVersion`, `kind` and `metadata`:

```yaml
sensitive:
  kinds: [generators.external-secrets.io/Fake]
```

This matters only for the content comparison, the one feature that reads full objects,
and only for kinds you add to `content.kinds`. A normal scan reads metadata only, never
`spec` or `data`. With `--compare-content`, an entry that matches no resource type on any
scanned cluster is reported on stderr, so a misspelling can't leave a credential-holding
kind's values in plain text.

## Built-in exclusions

These high-noise kinds are excluded by default:

- `Pod`, `apps/ReplicaSet`, `apps/ControllerRevision`
- `Event`, `events.k8s.io/Event`
- `Endpoints`, `discovery.k8s.io/EndpointSlice`
- `coordination.k8s.io/Lease`
- `Node`, `ComponentStatus`
- `metrics.k8s.io/NodeMetrics`, `metrics.k8s.io/PodMetrics`

The `kube-system`, `kube-public` and `kube-node-lease` namespaces are excluded by default
too; name one with `-n` to scan it anyway, and expect many control-plane objects there.
Your entries extend these lists rather than replacing them. Objects Kubernetes creates itself
(default RBAC roles, built-in APIServices and so on) are never reported unless someone
has edited them with `kubectl`, and objects with `ownerReferences` (made by a
controller) are always skipped: the parent is the interesting object.

## Scanning only some namespaces

To scan only specific namespaces, use `-n`/`--namespaces` (or `scope.namespaces.include`).
Named namespaces are always scanned — exclusions, including the built-in ones, don't
apply to them — and cluster-scoped kinds are unaffected. Exclusions, by contrast, are
applied after a cluster-wide listing, so they reduce noise but not the
[permissions](permissions.md) needed.

The full schema and validation rules are in
[ADR-0003](adr/0003-config-loading-and-schema.md).
