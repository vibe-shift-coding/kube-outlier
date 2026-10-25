// Package config defines kube-outlier's configuration schema, loading
// (flags > env > file > built-in defaults, per ADR-0003), and validation.
package config

import "time"

// KindRef is a parsed canonical kind reference. Wire syntax: "group/Kind"; core group is
// bare "Kind". Split on the FIRST '/'; no slash means core group. Kind is case-sensitive.
type KindRef struct {
	Group string
	Kind  string
}

// Config is the complete, layered configuration for one run. Every section is
// optional in the file; Default() fills anything left unset.
type Config struct {
	Clusters  ClustersConfig  `koanf:"clusters"`
	Scope     ScopeConfig     `koanf:"scope"`
	Analysis  AnalysisConfig  `koanf:"analysis"`
	Content   ContentConfig   `koanf:"content"`
	Sensitive SensitiveConfig `koanf:"sensitive"`
	Report    ReportConfig    `koanf:"report"`
}

// ClustersConfig selects the fleet: which kubeconfig contexts, from which
// files, and how long to wait for each cluster.
type ClustersConfig struct {
	Contexts       []string      `koanf:"contexts"`
	Kubeconfigs    []string      `koanf:"kubeconfigs"`
	AllContexts    bool          `koanf:"allContexts"`
	ProbeTimeout   time.Duration `koanf:"probeTimeout"`   // default 5s; the reachability check
	RequestTimeout time.Duration `koanf:"requestTimeout"` // default 30s; every other API request
}

// ScopeConfig decides which resource types and namespaces are scanned. It is
// fingerprinted into baselines and snapshots (ADR-0004).
type ScopeConfig struct {
	IncludeClusterScoped bool           `koanf:"includeClusterScoped"` // default false
	Namespaces           NamespaceScope `koanf:"namespaces"`
	Kinds                KindScope      `koanf:"kinds"`
}

// NamespaceScope restricts or excludes namespaces.
type NamespaceScope struct {
	Include         []string `koanf:"include"`         // if non-empty: list only these namespaces (per-namespace API calls); Exclude/ExcludePatterns then don't apply
	Exclude         []string `koanf:"exclude"`         // exact names
	ExcludePatterns []string `koanf:"excludePatterns"` // RE2, anchored by the tool (^...$)
}

// KindScope restricts or excludes resource kinds, in KindRef wire syntax.
type KindScope struct {
	Exclude []string `koanf:"exclude"` // KindRef wire syntax; merged with built-in defaults
	Include []string `koanf:"include"` // if non-empty: allowlist mode (built-in excludes still apply)
}

// AnalysisConfig tunes scoring and the fleet presence signal (ADR-0002).
type AnalysisConfig struct {
	CommonThreshold          float64 `koanf:"commonThreshold"`          // default 0.6; presence ratio >= this is "common", so absent clusters get missing-from-cluster findings
	RareThreshold            float64 `koanf:"rareThreshold"`            // default 0.34; presence ratio <= this is "rare" (fleet-rare)
	MinScore                 int     `koanf:"minScore"`                 // default 40; findings scoring below are suppressed
	ArgoInstanceLabelKey     string  `koanf:"argoInstanceLabelKey"`     // default "app.kubernetes.io/instance"
	ReportMissingFromCluster bool    `koanf:"reportMissingFromCluster"` // default true
}

// ContentConfig controls the optional content comparison (ADR-0006).
type ContentConfig struct {
	Enabled      bool     `koanf:"enabled"`
	Kinds        []string `koanf:"kinds"`
	IgnoreFields []string `koanf:"ignoreFields"`
}

// SensitiveConfig lists resource kinds whose values are handled like a
// Secret's (ADR-0009): every value is hashed in the content comparison and
// reported only as a per-run digest. The core Secret is always sensitive and
// needs no entry.
type SensitiveConfig struct {
	Kinds []string `koanf:"kinds"` // KindRef wire syntax; default empty
}

// SensitiveKindSet returns every kind handled like a Secret: the core Secret
// plus Sensitive.Kinds. Invalid entries are skipped; Validate reports them.
func (c *Config) SensitiveKindSet() map[KindRef]bool {
	set := map[KindRef]bool{{Kind: "Secret"}: true}
	for _, s := range c.Sensitive.Kinds {
		if kr, err := ParseKindRef(s); err == nil {
			set[kr] = true
		}
	}
	return set
}

// FailOn selects which findings make the run exit non-zero (interfaces.md §3b).
type FailOn string

// The FailOn values. Inventory covers likely-manual and missing-from-cluster
// findings; content covers content-differs.
const (
	FailOnNone      FailOn = "none"
	FailOnInventory FailOn = "inventory"
	FailOnContent   FailOn = "content"
	FailOnAny       FailOn = "any"
)

// Format is the report output format (-o/--output).
type Format string

// The supported output formats.
const (
	FormatTable    Format = "table"
	FormatJSON     Format = "json"
	FormatYAML     Format = "yaml"
	FormatMarkdown Format = "markdown"
)

// ReportConfig controls output format and exit-code gating.
type ReportConfig struct {
	Format Format `koanf:"format"` // default table
	FailOn FailOn `koanf:"failOn"` // default none
	// AllowPartial makes a partial run (skipped cluster or GVR) exit 0 instead
	// of 2 when nothing else triggers. Skips are still reported. Default false.
	AllowPartial bool `koanf:"allowPartial"`
}
