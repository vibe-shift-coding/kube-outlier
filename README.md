<h1 align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img src="assets/logo.svg" alt="kube-outlier" width="420">
  </picture>
</h1>

<p align="center">
  <strong>Find the Kubernetes resources a human created by hand and never cleaned up.</strong>
</p>

<p align="center">
  <a href="https://github.com/vibe-shift-coding/kube-outlier/actions/workflows/ci.yaml"><img src="https://github.com/vibe-shift-coding/kube-outlier/actions/workflows/ci.yaml/badge.svg" alt="CI"></a>
  <a href="https://github.com/vibe-shift-coding/kube-outlier/releases"><img src="https://img.shields.io/github/v/release/vibe-shift-coding/kube-outlier" alt="Release"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/vibe-shift-coding/kube-outlier" alt="Go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/vibe-shift-coding/kube-outlier" alt="License"></a>
</p>

If your clusters are deployed by Helm, Argo CD or Flux, anything *without* pipeline
ownership deserves a second look: the ConfigMap someone `kubectl apply`'d during an
incident, the debug NetworkPolicy from last quarter, the test CronJob nobody owns.
`kube-outlier` finds them, scores how likely each is to be a forgotten one-off, and
shows you exactly why.

```console
$ kube-outlier scan --contexts cluster-eu1
SCORE  KIND                             NAMESPACE/NAME         CLUSTER      OWNER    AGE   WHY
80     ConfigMap                        team-a/debug-dump      cluster-eu1  kubectl  137d  no-pipeline-markers,kubectl-manager,last-applied,age
75     networking.k8s.io/NetworkPolicy  team-a/temp-allow-all  cluster-eu1  kubectl  6d    no-pipeline-markers,kubectl-manager,kind-weight
2 finding(s), 14 below the minimum score.
```

## Features

- **Works on a single cluster.** Reads pipeline markers (Helm, Argo CD and Flux labels
  and annotations) and `managedFields` — the API
  server's record of which client wrote each field — so `kubectl` authorship still
  counts against a resource that carries pipeline labels, and the OWNER column shows it
  (`helm+kubectl`).
- **Gets sharper with more clusters.** Compares where each resource exists across your
  fleet: one-offs stand out, and a resource missing from one cluster becomes its own
  finding.
- **Optionally compares content across clusters.** Shows, field by field, where a
  shared resource differs from what most clusters run — a lagging image tag is the
  classic case.
- **Explains every score.** Each finding lists the signals behind it; `--why` shows the
  full evidence, including why something was *not* reported.
- **Built for CI.** Stable JSON output, meaningful exit codes, and committable baselines
  so one cluster can be checked against the fleet without access to the others.
- **Read-only by design.** Strictly read-only (its HTTP client refuses anything but GET
  and HEAD), talks to nothing but your API servers, never prints or stores Secret values,
  and works with namespace-only access.

## Install

Requires Go; the minimum version is in [go.mod](go.mod) and on the badge above. With Go's
default `GOTOOLCHAIN=auto`, an older Go downloads the toolchain version `go.mod` names
for the build, so the binary gets the standard-library security fixes it was tested
with:

```sh
go install github.com/vibe-shift-coding/kube-outlier/cmd/kube-outlier@latest
kube-outlier version
```

`go install` puts the binary in `$(go env GOPATH)/bin`; add that directory to your
`PATH` if `kube-outlier` is not found. To upgrade, run the same `go install` again; to
uninstall, delete that binary. kube-outlier keeps no state: no cache, and no files besides
the config and baseline files you create.

It runs on Linux, macOS and Windows. Until prebuilt binaries ship, installing needs Go;
binaries, a Homebrew tap and a `kubectl outlier` krew plugin are on the
[roadmap](ROADMAP.md).

It is tested on Kubernetes 1.35 (minikube) and 1.37 (kind, in CI). Older clusters should
work as long as they record `managedFields` (Kubernetes 1.18 and later), but aren't
tested. Managed clusters work too: kube-outlier reads your kubeconfig as `kubectl` does,
including exec credential plugins (`aws eks get-token`, `gke-gcloud-auth-plugin`,
`kubelogin`) and OIDC. See [troubleshooting](docs/troubleshooting.md#managed-clusters-eks-gke-aks)
for what to expect there.

## Quick start

`kube-outlier` uses the same kubeconfig and contexts as `kubectl`. It needs `list` access
to the resource types it scans (plus `get` for `--compare-content`), either across the
cluster or only in the namespaces you pass with `-n`. The surest access check is a scan itself: it never fails
on a missing permission, and names every type it couldn't read on stderr
(`… forbidden, skipped`). [docs/permissions.md](docs/permissions.md) shows how to check
beforehand and has a ready-made read-only role.

| To… | Run |
|---|---|
| See which clusters you can reach | `kube-outlier clusters` |
| Scan the current context | `kube-outlier scan` |
| Scan only some namespaces (required if you can only read some namespaces) | `kube-outlier scan -n team-a,team-b` |
| Scan a fleet — the fleet presence signal turns on automatically at 2+ clusters | `kube-outlier scan --contexts cluster-eu1,cluster-us1,cluster-ap1` |
| Also compare what shared resources contain across clusters | `kube-outlier scan --contexts cluster-eu1,cluster-us1,cluster-ap1 --compare-content` |
| See why something was (or wasn't) reported | `kube-outlier scan --why "ConfigMap team-a/debug-dump"` |
| Get JSON and a non-zero exit code when something turns up (CI) | `kube-outlier scan --fail-on inventory -o json` |

New to the tool? [docs/RUNNING.md](docs/RUNNING.md) walks through every feature against a
local three-cluster fleet. [docs/usage.md](docs/usage.md) explains each column of the
report.

## How it works

A scan scores every resource with two signals. Separately, it can also compare content
across clusters:

| | Question it answers | What it looks at | When it runs |
|---|---|---|---|
| **Ownership** signal | *Did a human create this by hand?* | Pipeline markers and `managedFields` | Always |
| **Fleet presence** signal | *Is it unusual for this to exist here?* | **Which clusters have** the resource | Automatically, with 2+ clusters or a baseline |
| **Content comparison** | *Do the copies of a shared resource match?* | **What the copies contain** | Opt-in with `--compare-content`, with 2+ clusters |

The two signals ask whether a resource *should exist*. The content comparison asks
whether copies of a resource that exists everywhere *contain the same thing*. It
compares clusters with each other, not with Git: Argo CD and Flux already detect a live
cluster differing from Git, and `kube-outlier` never reads Git.

The ownership signal scores each resource by ownership, kind (a stray
ClusterRoleBinding outranks a ConfigMap) and age. The fleet presence signal adjusts
those scores: rare with no pipeline markers is the strongest "forgotten here" signal,
present everywhere suggests a legitimate unlabelled resource, and a resource on most
clusters but **missing from one** is reported on its own:

```console
$ kube-outlier scan --contexts c1,c2,c3
SCORE  KIND                             NAMESPACE/NAME         PRESENT-ON  CLUSTER  OWNER    AGE   WHY
100    ConfigMap                        team-a/debug-dump      1/3         c1       kubectl  137d  no-pipeline-markers,kubectl-manager,last-applied,age,fleet-rare
95     networking.k8s.io/NetworkPolicy  team-a/temp-allow-all  1/3         c1       kubectl  6d    no-pipeline-markers,kubectl-manager,kind-weight,fleet-rare
50     networking.k8s.io/NetworkPolicy  team-a/default-deny    2/3         c3       -        41d   missing-from-cluster
3 finding(s), 21 below the minimum score.
```

"Most clusters" means at least `analysis.commonThreshold` of them (default 0.6, so 2 of 3).

The content comparison works out the version most clusters run and shows where a
cluster differs — for pipeline-managed resources too. It reads full objects, so it is
opt-in and covers the kinds in `content.kinds` (by default Deployments, ConfigMaps and
Ingresses):

```console
$ kube-outlier scan --contexts c1,c2,c3 --compare-content
SCORE  KIND             NAMESPACE/NAME  PRESENT-ON  CLUSTER  OWNER  AGE  WHY
-      apps/Deployment  team-a/api      3/3         c3       -      58d  content-differs

Content differences:

  apps/Deployment team-a/api — majority version on 2 cluster(s): c1, c2
    cluster c3:
      spec.template.spec.containers[0].image: api:1.8.2 → api:1.7.0

Fields that differ most:
  spec.template.spec.containers[0].image differs on 1 cluster(s)

1 finding(s), 21 below the minimum score.
```

Findings describe what is *unusual*, never what is *wrong*: the majority can be wrong,
so you decide. Two caveats:

- `managedFields` records the client, not the person. If your pipeline itself runs
  `kubectl apply`, its resources look hand-made too; pipeline markers or fleet presence
  are what tell them apart.
- On managed clusters (EKS, GKE, AKS), `--include-cluster-scoped` also surfaces objects
  the provider installed with `kubectl`. See
  [troubleshooting](docs/troubleshooting.md#managed-clusters-eks-gke-aks).

The full design is in [docs/DESIGN.md](docs/DESIGN.md).

## What it scans

There is no fixed list of kinds: a scan covers every resource type each cluster's API
serves, found through API discovery, minus a short list of high-noise kinds. That
includes custom resources, the objects your CRDs define, as well as built-in kinds.

| | Kinds |
|---|---|
| **Scanned by default** | Every namespaced type, built-in or custom (for example cert-manager `Certificate`s or Argo CD `Application`s) |
| **Opt-in**, with `--include-cluster-scoped` | Every cluster-scoped type, built-in or custom: ClusterRoles, ClusterRoleBindings, admission webhooks, StorageClasses, the CRDs themselves, and cluster-scoped custom resources (for example cert-manager `ClusterIssuer`s) |
| **Never scanned** | Pods, ReplicaSets, ControllerRevisions, Events, Endpoints, EndpointSlices, Leases, Nodes, ComponentStatus, metrics |
| **Excluded by default** | The `kube-system`, `kube-public` and `kube-node-lease` namespaces; name one with `-n` to scan it anyway (expect many control-plane objects there) |
| **Not reported** | Objects with `ownerReferences` (made by a controller), and objects Kubernetes creates itself unless someone edited them with `kubectl` |
| **Weighted higher** | Roles, RoleBindings, ClusterRoles, ClusterRoleBindings, NetworkPolicies, Secrets and admission webhooks; less so workloads, Services and Ingresses |
| **Content comparison** | Deployments, ConfigMaps and Ingresses (`content.kinds`) |

A normal scan reads metadata only (names, labels, annotations, `managedFields`), never
`spec` or `data`. To change the scope, see [docs/configuration.md](docs/configuration.md).

### When it can't read everything

A missing permission never stops a scan. What it can't read is skipped, named on stderr
(`… forbidden, skipped`), and the run exits `2`, or `0` with `--allow-partial`:

| Situation | What happens |
|---|---|
| No `list` access to a type, built-in or custom | That type is skipped on that cluster; everything else is scanned. In fleet comparisons that cluster is never counted as missing those resources. |
| Access only inside some namespaces | Pass them with `-n`; each is listed separately. Cluster-scoped types (with `--include-cluster-scoped`) are always listed cluster-wide, so they are skipped as forbidden. |
| An API group fails discovery (an aggregated API whose backing service is down) | Its types are skipped on that cluster and reported. |
| A custom resource type served on only some clusters (its CRD isn't installed everywhere) | Its resources are compared only among the clusters that serve the type, so they never look missing from the others. On a type only one cluster serves, they are scored on ownership alone. |

To make a deliberately off-limits type stop counting as partial, exclude it
(`scope.kinds.exclude`). [docs/permissions.md](docs/permissions.md) shows how to check
your access and has a ready-made read-only role.

## Using it in CI

```sh
kube-outlier scan --baseline baseline.yaml --fail-on inventory -o json > findings.json
```

| Exit | Meaning |
|---|---|
| `0` | Clean run |
| `1` | Fatal error: bad flags or config, unknown or duplicate context, no reachable cluster, baseline scope mismatch, a content gate that cannot run, or a `--why` resource not found |
| `2` | Partial run: a cluster, resource type or (with `--compare-content`) object couldn't be read and was skipped (`0` with `--allow-partial`) |
| `3` | Inventory findings (with `--fail-on inventory` or `any`) |
| `4` | Content differences (with `--fail-on content` or `any`) |

Baselines, reading the JSON with `jq`, a GitHub Actions example and the details of gating
a pipeline are covered in [docs/usage.md](docs/usage.md#using-it-in-ci).

## Permissions

Read-only access is all it needs: `list` on the resource types in scope — cluster-wide by
default, or only inside the namespaces you pass with `-n` — plus `get` on the
`content.kinds` resources if you use `--compare-content`. Anything it can't read is
reported and skipped, never fatal (see
[When it can't read everything](#when-it-cant-read-everything)). See
[docs/permissions.md](docs/permissions.md) for ready-made roles, including one that can't
read Secrets.

## Safety

These are design rules, enforced by tests and CI:

- **Read-only.** No code path calls a write method (`Create`, `Update`, `Patch`, `Apply`,
  `Delete` or their variants) on a Kubernetes client, and the HTTP client refuses any
  request that is not a GET or HEAD.
- **Secrets are never printed or stored** — not in reports, `--why` output or
  baselines. The content comparison compares Secret values as hashes and reports them
  only as digests keyed for that run, so nothing can be checked against a guessed value.
  Custom resources that hold credentials can get the same treatment through
  [`sensitive.kinds`](docs/configuration.md#sensitive-kinds).
- **Fail-soft.** An unreachable cluster, a forbidden resource type, an API group that
  fails discovery or an object the content comparison can't read is reported and
  skipped, never counted as "the resource is absent there" or as an unchanged copy.
- **Nothing leaves your machine** except requests to the API servers in your kubeconfig,
  and whatever your kubeconfig's login method contacts (an exec credential plugin or an
  OIDC provider), exactly as with `kubectl`. There is no telemetry and no update check.
  The only file it writes is the baseline you name with `--out`.

## How it compares

| Tool | How it differs |
|---|---|
| **Argo CD orphaned resources** | Built into Argo CD: opt-in per project, covering namespaced resources in the namespaces that project deploys to. `kube-outlier` needs only a kubeconfig, works with any deployment tool, and can include cluster-scoped kinds. |
| **Kor** | Finds *unused* resources, such as a ConfigMap no pod mounts. A different signal: a ConfigMap that is in use can still be a hand-made one-off. |
| **Popeye** | Lints a live cluster for misconfigurations and health problems. It asks "is this configured well?", not "who created this?". |
| **Policy engines (Kyverno, OPA Gatekeeper)** | Check resources against rules you write, both at admission and in background audits. You could approximate parts of the ownership signal with custom policies; `kube-outlier` ships the heuristics, the scoring and the cross-cluster comparison ready-made. |

These tools complement each other; running several of them is common.

## Documentation

| | |
|---|---|
| [Walkthrough](docs/RUNNING.md) | Every feature, step by step, against a local three-cluster fleet |
| [Usage](docs/usage.md) | Reading the output, `--why`, baselines, CI |
| [Configuration](docs/configuration.md) | Config file, environment variables, exclusions |
| [Permissions](docs/permissions.md) | Required access and a ready-made read-only role |
| [Troubleshooting](docs/troubleshooting.md) | Common problems and fixes |
| [Design](docs/DESIGN.md) | Signal model, scoring, normalization, content comparison |
| [Interfaces](docs/interfaces.md#3-json-report-schema-schemaversion-1) | JSON schema and exit codes (versioned) |
| [Design decisions](docs/adr/) | Why things are the way they are (ADRs) |

## Status

**Early release (pre-1.0).** The JSON schema and exit codes are versioned so CI
integrations don't break, but expect the scoring weights to be tuned before 1.0.
Planned work is on the [roadmap](ROADMAP.md).

## Contributing

Bug reports, ideas and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md).
Questions go in [Discussions](https://github.com/vibe-shift-coding/kube-outlier/discussions).
Please report security issues privately as described in [SECURITY.md](SECURITY.md),
not in a public issue.

## License

Apache License 2.0 — see [LICENSE](LICENSE).

Kubernetes® is a registered trademark of The Linux Foundation. kube-outlier is not
affiliated with or endorsed by The Linux Foundation or the CNCF.
