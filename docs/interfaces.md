# interfaces.md — canonical contracts for kube-outlier

> This file defines the stable contracts of kube-outlier: type shapes, package boundaries,
> the JSON output schema, and exit codes. Code must match it. Changing anything defined here
> (adding, renaming, removing or retyping) needs a reviewed pull request that updates this
> file first. If other documentation and this file disagree, this file wins.
>
> Optional fields (fleet presence, content-comparison detail, ownership, age, baseline-only
> counters) are omitted from JSON (`omitempty`) when the feature or data that fills them
> is not there.

---

## 1. Package layout and allowed imports

Import edges form a DAG. A package may import only what its row lists (plus stdlib).
`golang.org/x/sync/errgroup` is allowed anywhere concurrency is specified. Nothing outside
`cmd/` may import `report`. Nothing may import `cmd/`.

| package                | internal imports allowed              | external (non-stdlib) imports allowed |
|------------------------|---------------------------------------|----------------------------------------|
| `internal/identity`    | —                                     | — (pure Go; no k8s deps)               |
| `internal/orderedyaml` | —                                     | sigs.k8s.io/yaml (incl. its goyaml.v3)  |
| `internal/config`      | —                                     | koanf, spf13/pflag, sigs.k8s.io/yaml    |
| `internal/connect`     | config                                | k8s.io/client-go, k8s.io/apimachinery   |
| `internal/discovery`   | config, connect                       | k8s.io/client-go (discovery, restmapper), apimachinery |
| `internal/inventory`   | identity, config, connect, discovery  | k8s.io/client-go (metadata, dynamic), apimachinery |
| `internal/ownership`   | identity, inventory, config           | apimachinery                            |
| `internal/normalize`   | identity, config                      | apimachinery, sigs.k8s.io/yaml          |
| `internal/analyze`     | identity, inventory, ownership, config| apimachinery, wI2L/jsondiff (content)    |
| `internal/baseline`    | identity, ownership, config, orderedyaml | sigs.k8s.io/yaml                     |
| `internal/report`      | identity, inventory, ownership, analyze, connect, config, orderedyaml | apimachinery, sigs.k8s.io/yaml |
| `cmd/kube-outlier`     | all internal packages                 | cobra, pflag, k8s.io/klog/v2 (to route client-go's logs through slog), golang.org/x/term (terminal width for the table) |

Notes:
- The internal package is named `discovery`; import client-go's discovery as `k8sdiscovery`.
- `k8s.io/apimachinery` types permitted in exported signatures: `metav1.OwnerReference`,
  `metav1.ManagedFieldsEntry`, `metav1.Time`, `schema.GroupVersionResource`. No others.
- Read-only invariant: no code anywhere (tests exempt) calls — or takes a method value
  of — `Create`, `Update`, `Patch`, `Delete`, `Apply` (or their variants) on any
  Kubernetes client, or builds a raw REST request with a write verb. CI greps for this
  (`scripts/check-readonly.sh`); at runtime every client's transport refuses any HTTP
  method other than GET and HEAD.

---

## 2. Core types

### 2.1 `internal/identity`

```go
package identity

// ResourceKey is the fleet-wide identity of a resource. It is VERSION-AGNOSTIC:
// group+kind, never apiVersion, because served versions legitimately differ per cluster.
type ResourceKey struct {
	Group     string // "" for the core group
	Kind      string // e.g. "ConfigMap" (canonical CamelCase kind, not resource plural)
	Namespace string // "" for cluster-scoped resources
	Name      string
}

// String renders "group/Kind namespace/name" with core group as bare Kind and
// cluster-scoped as "Kind name". Used for map keys, sorting, --why lookup and
// baseline files.
func (k ResourceKey) String() string

// MarshalJSON writes {"group", "kind", "namespace", "name", "id"}, where id is
// String(); reading ignores id.
func (k ResourceKey) MarshalJSON() ([]byte, error)

// ParseKey is the inverse of String.
func ParseKey(s string) (ResourceKey, error)

// PresenceMatrix maps identities to the reachable clusters they exist on. Built only
// when N >= 2. Cluster names are connect.ClusterRef.Name values.
type PresenceMatrix struct {
	Clusters []string                          // all reachable clusters, stable sorted order; len == N
	ByKey    map[ResourceKey]map[string]bool   // key -> set of cluster names where present
}

func NewPresenceMatrix(clusters []string) *PresenceMatrix
func (m *PresenceMatrix) Add(k ResourceKey, cluster string)
func (m *PresenceMatrix) PresentOn(k ResourceKey) []string  // sorted
func (m *PresenceMatrix) MissingFrom(k ResourceKey) []string // sorted; Clusters − PresentOn
func (m *PresenceMatrix) Ratio(k ResourceKey) float64        // |PresentOn| / len(Clusters); 0 if unknown key
```

### 2.2 `internal/config`

```go
package config

// KindRef is a parsed canonical kind reference. Wire syntax: "group/Kind"; core group is
// bare "Kind". Split on the FIRST '/'; no slash means core group. Kind is case-sensitive.
type KindRef struct {
	Group string
	Kind  string
}

func ParseKindRef(s string) (KindRef, error) // errors on empty string, empty Kind, ">1 slash"

type Config struct {
	Clusters ClustersConfig `koanf:"clusters"`
	Scope    ScopeConfig    `koanf:"scope"`
	Analysis AnalysisConfig `koanf:"analysis"`
	Content   ContentConfig   `koanf:"content"`
	Sensitive SensitiveConfig `koanf:"sensitive"`
	Report    ReportConfig    `koanf:"report"`
}

type ClustersConfig struct {
	Contexts       []string      `koanf:"contexts"`
	Kubeconfigs    []string      `koanf:"kubeconfigs"`
	AllContexts    bool          `koanf:"allContexts"`
	ProbeTimeout   time.Duration `koanf:"probeTimeout"`   // default 5s
	RequestTimeout time.Duration `koanf:"requestTimeout"` // default 30s; every other API request
}

type ScopeConfig struct {
	IncludeClusterScoped bool           `koanf:"includeClusterScoped"` // default false
	Namespaces           NamespaceScope `koanf:"namespaces"`
	Kinds                KindScope      `koanf:"kinds"`
}

type NamespaceScope struct {
	Include         []string `koanf:"include"`         // if non-empty: list only these namespaces (per-namespace API calls); Exclude/ExcludePatterns then don't apply
	Exclude         []string `koanf:"exclude"`         // exact names
	ExcludePatterns []string `koanf:"excludePatterns"` // RE2, anchored by the tool (^...$)
}

type KindScope struct {
	Exclude []string `koanf:"exclude"` // KindRef wire syntax; merged with built-in defaults
	Include []string `koanf:"include"` // if non-empty: allowlist mode (built-in excludes still apply)
}

type AnalysisConfig struct {
	CommonThreshold          float64 `koanf:"commonThreshold"`          // default 0.6; ratio >= this is "common"
	RareThreshold            float64 `koanf:"rareThreshold"`            // default 0.34; ratio <= this is "rare"
	MinScore                 int     `koanf:"minScore"`                 // default 40; findings scoring below are suppressed
	ArgoInstanceLabelKey     string  `koanf:"argoInstanceLabelKey"`     // default "app.kubernetes.io/instance"
	ReportMissingFromCluster bool    `koanf:"reportMissingFromCluster"` // default true
}

type ContentConfig struct {
	Enabled      bool     `koanf:"enabled"`
	Kinds        []string `koanf:"kinds"`
	IgnoreFields []string `koanf:"ignoreFields"`
}

// SensitiveConfig lists kinds handled like a Secret (ADR-0009). The core Secret is
// always sensitive and needs no entry.
type SensitiveConfig struct {
	Kinds []string `koanf:"kinds"` // KindRef wire syntax; default empty
}

// SensitiveKindSet returns the core Secret plus Sensitive.Kinds (invalid entries skipped).
func (c *Config) SensitiveKindSet() map[KindRef]bool

// DiscoveryOptional reports whether a failed discovery of an API group may be
// ignored: a built-in list (the aggregated metrics APIs) in defaults.yaml.
func DiscoveryOptional(group string) bool

// IsBuiltinKindExclude reports whether ref is a built-in scope.kinds.exclude
// entry (defaults.yaml), never reported as matching nothing.
func IsBuiltinKindExclude(ref string) bool

type FailOn string
const (
	FailOnNone      FailOn = "none"
	FailOnInventory FailOn = "inventory"
	FailOnContent   FailOn = "content"
	FailOnAny       FailOn = "any"
)

type Format string
const (
	FormatTable    Format = "table"
	FormatJSON     Format = "json"
	FormatYAML     Format = "yaml"
	FormatMarkdown Format = "markdown"
)

type ReportConfig struct {
	Format       Format `koanf:"format"`       // default table
	FailOn       FailOn `koanf:"failOn"`       // default none; content needs content.enabled, any gates on content only when enabled (see §3b)
	AllowPartial bool   `koanf:"allowPartial"` // default false; partial run exits 0 instead of 2
}
```

Loading is strict: a key in the config file, or a `KUBE_OUTLIER_*` environment variable,
that names no setting is a fatal error (exit 1).

### 2.3 `internal/connect`

```go
package connect

// ClusterRef is one member of the requested fleet, after resolution and probing.
type ClusterRef struct {
	Name          string // stable display name; unique within a run (context name by default)
	Context       string // kubeconfig context name
	Kubeconfig    string // path of the kubeconfig file it came from
	Reachable     bool
	ServerVersion string // e.g. "v1.30.2"; "" when unreachable
	SkipReason    string // non-empty iff !Reachable; human-readable cause
	// (unexported: the kubeconfig files of the merge, so clients load the
	// context exactly as KUBECONFIG=a:b would)
}

// Clients is the read-only client bundle for one reachable cluster. Every
// request is bounded by clusters.requestTimeout; the transport refuses any
// method other than GET and HEAD. API server warnings go to slog.
type Clients struct {
	Dynamic   dynamic.Interface
	Metadata  metadata.Interface
	Discovery k8sdiscovery.DiscoveryInterface
}
```

### 2.4 `internal/inventory`

```go
package inventory

// Resource is the metadata-only record from Pass A. One per (cluster, object).
// The kubectl.kubernetes.io/last-applied-configuration annotation is kept with an
// EMPTY value: its presence is evidence, its value can hold Secret data.
type Resource struct {
	Key               identity.ResourceKey
	Cluster           string // connect.ClusterRef.Name
	APIVersion        string // as served on THIS cluster, e.g. "apps/v1" (informational only)
	UID               string
	Labels            map[string]string
	Annotations       map[string]string
	OwnerReferences   []metav1.OwnerReference
	ManagedFields     []metav1.ManagedFieldsEntry
	CreationTimestamp metav1.Time
}

// Snapshot is everything collected from one cluster in one run.
type Snapshot struct {
	Cluster   string
	Resources []Resource
	Errors    []GVRError // GVRs that could not be listed (forbidden, timeout, ...)
}

type GVRError struct {
	GVR       schema.GroupVersionResource
	Namespace string `json:",omitempty"` // "" = cluster-wide list; set when scope.namespaces.include lists per namespace
	Err       string
	Forbidden bool // true for 403: report as "forbidden, skipped", never fatal
}

// FetchBody is Pass B: ONE targeted full-object Get, never a list. Used
// only by --compare-content, for the content.kinds resources it compares.
// Bodies travel as plain maps (see the apimachinery signature whitelist above).
func FetchBody(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, key identity.ResourceKey) (map[string]interface{}, error)
```

### 2.5 `internal/ownership`

```go
package ownership

type Owner string
const (
	OwnerHelm       Owner = "helm"
	OwnerArgoCD     Owner = "argocd"
	OwnerFlux       Owner = "flux"
	OwnerController Owner = "controller" // non-empty ownerReferences
	OwnerKubectl    Owner = "kubectl"
	OwnerUnknown    Owner = "unknown"
)

// Signal is one piece of classification evidence. Code values are a frozen enum —
// see §4. Detail is free-form human text ("manager=kubectl-client-side-apply").
type Signal struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// OwnershipVerdict is the full ownership classification for one Resource.
type OwnershipVerdict struct {
	Owner          Owner    `json:"owner"`          // primary owner by precedence: controller > flux > argocd > helm > kubectl > unknown
	KubectlWritten bool     `json:"kubectlWritten"` // true iff any managedFields manager has prefix "kubectl" (records the client, not the person)
	Managers       []string `json:"managers"`       // distinct managedFields manager names, sorted
	Signals        []Signal `json:"signals"`        // every signal that fired, stable order (marker table order)
}
```

### 2.6 `internal/analyze`

```go
package analyze

// ScoreReason explains one score contribution. Code values frozen in §4.
type ScoreReason struct {
	Code   string `json:"code"`
	Points int    `json:"points"` // signed
	Detail string `json:"detail,omitempty"`
}

type Score struct {
	Base          int           `json:"base"`          // ownership-signal sum before clamping
	FleetModifier int           `json:"fleetModifier"` // 0 when the fleet presence signal did not apply
	Total         int           `json:"total"`         // clamp(Base+FleetModifier, 0, 100)
	FleetApplied  bool          `json:"fleetApplied"`
	Reasons       []ScoreReason `json:"reasons"`
}

type FindingClass string
const (
	ClassLikelyManual       FindingClass = "likely-manual"        // one resource, probably created by hand
	ClassMissingFromCluster FindingClass = "missing-from-cluster" // common resource absent from a cluster
	ClassContentDiffers     FindingClass = "content-differs"
)

// Presence is nil unless the fleet presence signal (live or baseline) applied. Never
// fabricate it. In baseline mode the name lists are empty (a baseline stores counts).
type Presence struct {
	PresentOn    []string `json:"presentOn"`    // cluster names, sorted
	MissingFrom  []string `json:"missingFrom"`  // cluster names, sorted
	PresentCount int      `json:"presentCount"` // always exact; == len(PresentOn) in live mode
	FleetSize    int      `json:"fleetSize"`    // N (reachable) or baseline cluster count
	Ratio        float64  `json:"ratio"`
}

type Finding struct {
	Class              FindingClass                `json:"class"`
	Key                identity.ResourceKey        `json:"resource"`
	Clusters           []string                    `json:"clusters"` // clusters this finding is about, sorted; for missing-from-cluster, where it is MISSING
	Ownership          *ownership.OwnershipVerdict `json:"ownership,omitempty"` // nil for missing-from-cluster, content-differs
	Score              Score                       `json:"score"`
	Presence           *Presence                   `json:"presence,omitempty"`
	Content            *ContentDetail              `json:"content,omitempty"`  // only on Class==content-differs
	CreationTimestamps map[string]metav1.Time      `json:"creationTimestamps"` // per cluster name
	AgeDays            *int                        `json:"ageDays,omitempty"`  // now − oldest CreationTimestamp, whole days; nil when no timestamp is known
	Message            string                      `json:"message"`            // one-line neutral summary ("unusual", never "wrong")
}

// ContentDetail is the reference version + per-cluster differences for one identity (JSON key "content").
// content-differs findings carry a zero Score (reasons []) and are never suppressed by
// minScore; they sort after scored findings. The comparison is live-only: it needs two or
// more reachable clusters (a baseline stores no content).
type ContentDetail struct {
	MajorityClusters []string            `json:"majorityClusters"`    // clusters sharing the reference body, sorted
	MajorityHash     string              `json:"majorityHash"`        // canonical sha256 of the reference version (keyed per run for Secrets)
	Ambiguous        bool                `json:"ambiguous,omitempty"` // no true majority: largest group tied, or holds no more than half the copies
	Differing        []ClusterDifference `json:"differing"`           // per differing cluster, sorted by cluster name
}

type ClusterDifference struct {
	Cluster string    `json:"cluster"`
	Hash    string    `json:"hash"`          // canonical-content sha256 of this cluster's copy
	Ops     []FieldOp `json:"ops,omitempty"` // majority → this cluster
}

// FieldOp is one field-level change, RFC-6902-shaped (op+path+value=clusterValue) so
// the array is machine-consumable per ADR-0001, plus majorityValue for human rendering.
// Secret data values are never present: they appear as "hmac-sha256:<16 hex>" digests
// keyed with a random per-run key (equal within one report, never checkable against a guess).
type FieldOp struct {
	Op            string      `json:"op"`                      // add | remove | replace
	Path          string      `json:"path"`                    // RFC 6901 JSON pointer
	MajorityValue interface{} `json:"majorityValue,omitempty"` // reference value (absent for "add")
	ClusterValue  interface{} `json:"clusterValue,omitempty"`  // this cluster's value (absent for "remove")
}
```

### 2.7 `internal/baseline`

```go
package baseline

// ScopeFingerprint captures exactly the scan scope that produced a baseline.
// Hash = sha256 hex of the canonical JSON (sorted keys, no whitespace) of the other
// fields. A scan against a baseline MUST compare fingerprints (see ADR-0004).
type ScopeFingerprint struct {
	IncludeClusterScoped     bool     `json:"includeClusterScoped"`
	NamespaceInclude         []string `json:"namespaceInclude,omitempty"` // sorted, deduped; omitted (and hash unchanged) when not restricting
	NamespaceExclude         []string `json:"namespaceExclude"`         // sorted
	NamespaceExcludePatterns []string `json:"namespaceExcludePatterns"` // sorted
	KindExclude              []string `json:"kindExclude"`              // canonical KindRef syntax, sorted, built-ins merged in
	KindInclude              []string `json:"kindInclude"`              // sorted
	Hash                     string   `json:"hash"`
}

// Provenance is the mandatory baseline header.
type Provenance struct {
	SchemaVersion int              `json:"schemaVersion"` // 1
	ToolVersion   string           `json:"toolVersion"`
	GeneratedAt   time.Time        `json:"generatedAt"`
	ClusterCount  int              `json:"clusterCount"`  // contributing (reachable) clusters
	Clusters      []string         `json:"clusters"`      // contributing cluster names, sorted
	Scope         ScopeFingerprint `json:"scope"`
}

// ResourceRef is a ResourceKey written as its identity string
// ("apps/Deployment shop/api"); reading also accepts {group, kind, namespace, name}.
type ResourceRef identity.ResourceKey

type Entry struct {
	Key              ResourceRef             `json:"resource"`
	ClusterCount     int                     `json:"clusterCount"`  // clusters it was present on
	Ratio            float64                 `json:"ratio"`         // ClusterCount / Provenance.ClusterCount — exact by construction: kinds not cleanly listable (served, no GVRError) on EVERY contributing cluster are excluded from the baseline entirely (clean-listing rule)
	OwnershipSummary map[ownership.Owner]int `json:"ownershipSummary"` // per-cluster verdict tally
}

type ApprovedException struct {
	Key        ResourceRef `json:"resource"`
	Reason     string      `json:"reason"`     // required, non-empty
	ApprovedBy string      `json:"approvedBy"` // required, non-empty
	Expires    *time.Time  `json:"expires,omitempty"` // nil = never
}

// Field order is file order: hand-edited exceptions above the generated entries.
type Baseline struct {
	Provenance Provenance          `json:"provenance"`
	Exceptions []ApprovedException `json:"exceptions,omitempty"`
	Entries    []Entry             `json:"entries"`    // sorted by Key.String()
}
```

### 2.8 `internal/normalize`

```go
package normalize

// Normalize returns a deep-copied body with server bookkeeping stripped
// (global + per-kind lists embedded in defaults.yaml). Pure: never mutates
// its input. Content-comparison canonicalization layers on top of this.
func Normalize(body map[string]interface{}, key identity.ResourceKey) map[string]interface{}

// --- content-comparison canonicalization (ADR-0006, ADR-0007) ---

// CanonicalizeForComparison returns the content-comparison form of a body: Normalize,
// then per-kind server-default strips (comparisonPerKind), user extraIgnore paths,
// quantity canonicalization, empty-map/slice removal, and Secret-data hashing.
// sensitive marks a sensitive.kinds kind other than the core Secret: every value
// outside apiVersion, kind and metadata is hashed instead (ADR-0009).
// Pure. Normalize alone stays conservative — only this over-strips.
func CanonicalizeForComparison(body map[string]interface{}, key identity.ResourceKey, extraIgnore []string, sensitive bool) map[string]interface{}

// CanonicalJSON marshals a body to canonical JSON: map keys sorted, no
// insignificant whitespace, array order PRESERVED.
func CanonicalJSON(body map[string]interface{}) ([]byte, error)

// CanonicalHash is the sha256 hex of CanonicalJSON — the content hash for majority
// grouping in the content comparison.
func CanonicalHash(body map[string]interface{}) (string, error)

// HashSecretData replaces each Secret data/stringData value with "sha256:<hex>"
// (the comparison form). Second return reports whether anything was hashed.
func HashSecretData(body map[string]interface{}) (map[string]interface{}, bool)

// PathMatches reports whether path (content.ignoreFields syntax) addresses at
// least one value present in body, so a path that never matches can be
// reported.
func PathMatches(body map[string]interface{}, path string) bool
```

Field paths (`content.ignoreFields` and the strip lists): dot-separated keys, `[*]` for
every element of a list, `[N]` for one element, `["key.with.dots"]` for a key containing
dots, a trailing `.*` for every value of a map; or a JSON pointer (RFC 6901) starting with
`/`, the form the JSON report's `path` uses.

---

## 3. JSON report schema (`schemaVersion: 1`)

Emitted by `--output json`; `--output yaml` is the identical structure and field order.
Additive changes only within schemaVersion 1; renames/removals require bumping to 2 and an
explicit, reviewed change. All field names camelCase. Top-level envelope:

```json
{
  "schemaVersion": 1,
  "toolVersion": "v0.1.0",
  "generatedAt": "2026-07-01T00:00:00Z",
  "fleet": {
    "requested": 2,
    "reachable": 1,
    "clusters": [
      {"name": "cluster-eu1", "context": "cluster-eu1", "reachable": true,  "serverVersion": "v1.30.2"},
      {"name": "stale",    "context": "stale",    "reachable": false, "skipReason": "dial tcp 127.0.0.1:1: connection refused"}
    ]
  },
  "scope": { "includeClusterScoped": false, "namespaceExclude": ["kube-system"], "namespaceExcludePatterns": [], "kindExclude": ["Event"], "kindInclude": [] },
  "fleetPresence": { "applied": false, "source": "none" },
  "skippedGVRs": [ {"cluster": "cluster-eu1", "gvr": "v1/secrets", "reason": "forbidden", "forbidden": true} ],
  "findings": [
    {
      "class": "likely-manual",
      "resource": {"group": "", "kind": "ConfigMap", "namespace": "default", "name": "debug-dump", "id": "ConfigMap default/debug-dump"},
      "clusters": ["cluster-eu1"],
      "ownership": {
        "owner": "kubectl",
        "kubectlWritten": true,
        "managers": ["kubectl-client-side-apply"],
        "signals": [
          {"code": "kubectl.manager", "detail": "manager=kubectl-client-side-apply"},
          {"code": "kubectl.last-applied", "detail": "kubectl.kubernetes.io/last-applied-configuration present"},
          {"code": "no-pipeline-markers", "detail": "no Helm, Argo CD or Flux markers, no ownerReferences"}
        ]
      },
      "score": {
        "base": 80, "fleetModifier": 0, "total": 80, "fleetApplied": false,
        "reasons": [
          {"code": "no-pipeline-markers", "points": 35},
          {"code": "kubectl-manager", "points": 25},
          {"code": "last-applied", "points": 10},
          {"code": "age", "points": 10, "detail": "137d"}
        ]
      },
      "creationTimestamps": {"cluster-eu1": "2026-02-14T09:30:00Z"},
      "ageDays": 137,
      "message": "ConfigMap default/debug-dump was written by a kubectl client and has no pipeline markers"
    }
  ],
  "summary": { "total": 1, "byClass": {"likely-manual": 1}, "suppressedBelowMinScore": 4 },
  "partial": true,
  "exitCode": 2
}
```

Rules:
- `fleetPresence.source` ∈ `"none" | "live" | "baseline"`.
- `resource.id` is the identity string `--why` and baseline exceptions take
  (`"apps/Deployment shop/api"`); the other four fields are the identity.
- `partial` is true when anything was skipped: a requested cluster
  (`fleet.reachable < fleet.requested`), an entry in `skippedGVRs`, or an entry in
  `skippedObjects`. It does not depend on `allowPartial`, which changes only `exitCode`.
- `summary.suppressedByException` (int, omitempty; ADR-0004):
  count of findings suppressed by a baseline's approved-exceptions block. Absent
  whenever zero, so it never appears in non-baseline output.
- `findings` sorted by `score.total` desc, then `resource` key string asc. Content-differs
  findings carry a zero `score` and are never suppressed by `minScore`, so
  they sort after scored findings; `content` is populated only on `class: "content-differs"`.
- `score.reasons` is always an array, `[]` when no reason applies
  (`missing-from-cluster`, `content-differs`); never `null`.
- `presence` appears on findings only when `fleetPresence.applied` is true.
- `ownership` is present only on `likely-manual` findings; `ageDays` only when a creation
  time is known (absent for baseline-mode `missing-from-cluster`).
- `clusters`: for `likely-manual`, where the resource is; for `missing-from-cluster`, where
  it is missing; for `content-differs`, where the copy differs.
- `scope.namespaceInclude` (string array, omitempty): present only when the scan was
  restricted to named namespaces (`--namespaces` / `scope.namespaces.include`).
- `skippedGVRs[].namespace` (string, omitempty): present only for per-namespace listings,
  naming the namespace the GVR could not be listed in.
- `skippedGVRs[].gvr` ends in `/*` (for example `example.com/v1/*`) when a whole API group
  version failed discovery: its types are unknown on that cluster, so none were listed.
  The aggregated metrics APIs (`config.DiscoveryOptional`) are exempt.
- `skippedObjects` (array, omitempty): the copies the content comparison could not read,
  each `{"cluster", "resource", "reason", "forbidden"}` with `resource` shaped like a
  finding's. A copy deleted since it was listed is not included. Absent without
  `--compare-content` or when every copy was read.
- Secret values never appear anywhere in this document, in any field, ever — nor any
  digest of one that could be checked against a guessed value.

## 3b. Exit codes (frozen)

| code | meaning | notes |
|------|---------|-------|
| 0 | clean run | no failOn trigger, no skipped clusters |
| 1 | fatal | bad flags/config (including an unknown config key or `KUBE_OUTLIER_*` variable), unknown context, a context listed twice, zero reachable clusters, baseline scope mismatch, a `--why` resource not found in scope, `failOn: content` when the content comparison is not enabled or cannot run (fewer than two reachable clusters, no `content.kinds` entry matching a type the scan listed, or none of the copies it needed could be read) |
| 2 | partial run | ≥1 requested cluster skipped OR ≥1 GVR or API group skipped OR ≥1 object the content comparison could not read (`skippedObjects`), and nothing else triggered; 0 instead when `allowPartial` is true |
| 3 | inventory findings | `failOn` ∈ {inventory, any} and ≥1 finding of class likely-manual/missing-from-cluster |
| 4 | content differences | `failOn` ∈ {content, any} and ≥1 content-differs finding (and no code-3 trigger) |

Precedence when several apply: **1 > 3 > 4 > 2 > 0**. With `failOn: none` (default), findings never affect the exit code; partial runs still return 2 unless `allowPartial` is true. `failOn: any` without the content comparison gates on inventory findings only and warns on stderr. `--why` ignores `failOn`.

## 3c. `clusters -o json` (`schemaVersion: 1`)

`kube-outlier clusters -o json|yaml` emits the scan report's `fleet` block with the same
provenance header. Same versioning rules as §3.

```json
{
  "schemaVersion": 1,
  "toolVersion": "v0.1.0",
  "generatedAt": "2026-07-01T00:00:00Z",
  "fleet": {
    "requested": 2,
    "reachable": 1,
    "clusters": [
      {"name": "cluster-eu1", "context": "cluster-eu1", "reachable": true, "serverVersion": "v1.30.2"},
      {"name": "stale", "context": "stale", "reachable": false, "skipReason": "dial tcp 127.0.0.1:1: connection refused"}
    ]
  }
}
```

---

## 4. Frozen string enums

**Signal codes** (`ownership.Signal.Code`):
`helm.managed-by-label`, `helm.release-annotation`, `helm.manager`,
`argocd.tracking-id`, `argocd.instance-label`, `argocd.manager`,
`flux.kustomize-label`, `flux.helm-label`, `flux.manager`,
`controller.owner-references`, `kubectl.manager`, `kubectl.last-applied`,
`no-pipeline-markers`.

**Score reason codes** (`analyze.ScoreReason.Code`):
`no-pipeline-markers`, `kubectl-manager`, `last-applied`, `kind-weight`, `age`,
`fleet-rare`, `fleet-common` (fleet presence signal only).

**Finding classes**: `likely-manual`, `missing-from-cluster`, `content-differs`.

**Owner values**: `helm`, `argocd`, `flux`, `controller`, `kubectl`, `unknown`.

Exact marker → signal mapping and owner precedence: ADR-0005. Exact points: ADR-0002.
Names: ADR-0007 and ADR-0008.
