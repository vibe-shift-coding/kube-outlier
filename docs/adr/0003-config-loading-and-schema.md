# ADR-0003: koanf for config; canonical kind syntax; layering rules

## Context
Configuration needs a loading library with clear precedence, and one notation for kinds.
Kubernetes tooling uses several (`apps/Deployment` as group/Kind, `v1/Event` as
version/Kind); mixing them across config, flags and the baseline scope fingerprint would
make equality checks unreliable.

## Decision

**Library: `github.com/knadh/koanf/v2`** with providers `file`, `env`, `confmap`
(explicitly set flags) and `structs` (built-in defaults), and the `yaml` parser.
- Rationale: koanf has no package-level global state, a far smaller transitive dependency
  graph (the "keep the module graph small" convention), and clean precedence via load
  order. viper's ubiquity was the counter-argument; rejected because dependency hygiene matters
  more here than familiarity.

**Layering (highest wins): flags > env > config file > built-in defaults.**
- Env prefix `KUBE_OUTLIER_`, `__` as section separator (e.g.
  `KUBE_OUTLIER_REPORT__FORMAT=json`). Env names are case-insensitive: each is mapped
  onto the canonical camelCase key (`KUBE_OUTLIER_ANALYSIS__MINSCORE` →
  `analysis.minScore`). List values are comma-separated.
- Config file: `--config path`, else `./.kube-outlier.yaml`, else
  `$XDG_CONFIG_HOME/kube-outlier/config.yaml` (`~/.config` when unset), else defaults
  only. A file found in a default location is named on stderr. A `--config` path that
  doesn't exist is fatal (exit 1); a missing *default-location* file is not.

**Canonical kind syntax: `group/Kind`**, core group written as bare `Kind`.
- Examples: `ConfigMap`, `Event`, `apps/Deployment`, `networking.k8s.io/Ingress`.
- Parse rule: split on the FIRST `/`; no slash → core group. More than one `/`, empty
  string, or empty Kind → validation error (fatal). Kind is case-sensitive CamelCase.
- Never version-qualified (`v1/Event` is invalid): identities are version-agnostic per
  `docs/interfaces.md`.

**Built-in default kind exclusions** (data file `internal/config/defaults.yaml`, embedded
via `go:embed`, merged with user `scope.kinds.exclude`; user `include` allowlist does not
override built-ins):
`Event`, `events.k8s.io/Event`, `discovery.k8s.io/EndpointSlice`, `Endpoints`,
`coordination.k8s.io/Lease`, `Pod`, `apps/ReplicaSet`, `apps/ControllerRevision`,
`metrics.k8s.io/NodeMetrics`, `metrics.k8s.io/PodMetrics`, `ComponentStatus` (deprecated
and virtual; listing it also prints a deprecation warning), `Node` (registered by the
kubelet, never a deployable artifact). (Jobs owned by CronJobs are handled by the
ownerReferences rule, not kind exclusion; objects the control plane creates itself are
handled by the system-object rule in ADR-0005.)

**Built-in default namespace exclusions:** `kube-system`, `kube-public`,
`kube-node-lease`. Users extend via config; a `scope.namespaces.exclude: []` in the file
does NOT remove built-ins (removal requires an explicit future flag — out of scope).

**Discovery failures.** An API group version whose discovery fails on a cluster (an
aggregated API whose service is down) is reported as a skipped type, `<group>/<version>/*`,
and makes the run partial (exit 2): its kinds are unknown there and are never counted as
absent. The aggregated metrics APIs (`metrics.k8s.io`, `custom.metrics.k8s.io`,
`external.metrics.k8s.io`, listed in `internal/config/defaults.yaml`) are exempt: their
types are never scanned, and their add-ons are often down.

**Namespace allowlist:** `scope.namespaces.include` (flag `-n`/`--namespaces`). When
non-empty, namespaced types are listed with one API call per named namespace instead of
cluster-wide, so namespace-scoped RBAC (a `RoleBinding`) is sufficient; the namespace
exclude lists, built-ins included, do not apply — naming a namespace is explicit intent.
Cluster-scoped types are unaffected (still governed by `includeClusterScoped`, still
listed cluster-wide). Part of the scope fingerprint (omitted when empty, so unrestricted
fingerprints do not depend on it).

**Unknown keys are fatal.** A config-file key (matched case-insensitively) or a
`KUBE_OUTLIER_*` environment variable that names no setting stops the run with exit 1,
naming it. A typo must never silently fall back to the default.

**Validation** (`Config.Validate()`, all violations fatal): thresholds in (0,1];
`rareThreshold < commonThreshold`; `minScore` in [0,100]; timeouts positive; every kind ref
parses; every `scope.namespaces.include` entry is a valid namespace name (RFC 1123 label);
every namespace pattern compiles as RE2 (patterns are anchored `^...$` by the
tool); `report.format`/`report.failOn` in their enums; `--contexts` and `--all-contexts`
mutually exclusive.

## Consequences
- One notation everywhere (config, flags, baseline scope fingerprint, docs); the
  fingerprint hash in ADR-0004 depends on this being stable.
- Code and tests share one parse function instead of re-implementing the convention.
- Cost: contributors used to viper adapt; acceptable.
