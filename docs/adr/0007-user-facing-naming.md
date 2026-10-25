# ADR-0007: User-facing naming — signals, content comparison, and fail-on groups

## Context
The tool's names appear on many surfaces: flags, finding classes, `--fail-on` values,
config keys, WHY reasons, exit-code text and the README. Several of those surfaces take a
single token, so a name cannot rely on a qualifier to be understood. The names must be
unambiguous to someone reading them cold at a terminal, must not collide with terms
Kubernetes tooling already owns, and must stay neutral (findings describe what is
unusual, never what is wrong). The tool's main job is finding forgotten one-offs; the
cross-cluster content comparison is an optional add-on.

Alternatives considered:

- **Numbered tiers (Tier 1/2/3)** for the three analyses. Comparable tools (Trivy,
  kube-score, Polaris, Kubescape) name their analyses rather than number them; numbers
  suggest ranked layers or paid plans, and hide that the fleet presence analysis adjusts
  ownership scores rather than running as a separate check.
- **"drift"** for the content comparison. Argo CD, Flux, Terraform and Puppet use it for
  live state versus desired state, and the ownership signal already reports what GitOps
  users call drift (a `kubectl edit` on a Helm resource). It also implies a correct
  position to drift away from, while the reference here is only the majority.
- **skew** (reads as Kubernetes version skew), **divergence** and **mismatch** (a
  resource missing from one cluster is also a divergence, so they do not separate
  presence from content), **diff** (`kubectl diff`, Argo CD diff), **consensus** (names
  the reference rather than the finding; suggests etcd/Raft), **parity** and
  **compliance** (judgemental), **content-outlier** (every finding class is an outlier).
- **An `outliers` fail-on group** for the presence and ownership classes. A cluster
  whose copy differs from the majority is an outlier too, so the group name would not
  match its contents.

## Decision
User-facing docs describe a scan as **two signals** plus an **optional comparison**,
split by what each looks at — *which clusters have* a resource versus *what the copies
contain*:

| Part | Name | DESIGN.md |
|---|---|---|
| Ownership markers and `managedFields` | **ownership signal** | Tier 1 |
| Presence across clusters or a baseline | **fleet presence signal** | Tier 2 |
| Field-by-field comparison of shared copies | **content comparison** | Tier 3 |

The vocabulary on each surface (frozen in `interfaces.md`):

| Surface | Name |
|---|---|
| Scan flag | `--compare-content` |
| Finding class and WHY reason | `content-differs` |
| `--fail-on` / `report.failOn` | `none \| inventory \| content \| any` |
| Exit 3 / 4 | inventory findings / content differences |
| Config section | `content.*` |
| JSON finding detail | `content`, with `majorityClusters`, `majorityHash`, `differing` |
| Report section heading | `Content differences:` |

`inventory` covers `likely-manual` and `missing-from-cluster`;
`content` covers `content-differs`; `any` covers both. The comparison is a `scan` flag
(one connection pass, one exit code) with its own section in the README and help text.

The code uses the same vocabulary so contributors can find it from the docs:
`compare_content.go` in `cmd/kube-outlier`, `content.go` in `internal/analyze` and
`internal/report`, types `ContentDetail`, `ClusterDifference` and `FieldOp`, and
`normalize.CanonicalizeForComparison` with its `comparisonPerKind` strip data.
DESIGN.md, the ADRs and the `Tier1Findings`/`Tier2Findings` functions use the tier
numbers.

## Consequences
Every user-facing name says what it compares, and none collides with a term Kubernetes
tooling already owns. "GitOps drift" appears only in prose that says this is not it.
The cost is that design documents use tier numbers that user docs do not — acceptable
because the design documents are for contributors. Revisit if the content comparison
becomes a core part of the product rather than an add-on; a separate `compare`
subcommand was considered and not chosen.
