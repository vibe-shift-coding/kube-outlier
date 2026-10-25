package config

import (
	_ "embed"
	"time"

	"sigs.k8s.io/yaml"
)

//go:embed defaults.yaml
var defaultsYAML []byte

type builtinDefaults struct {
	KindExclude             []string `json:"kindExclude"`
	NamespaceExclude        []string `json:"namespaceExclude"`
	DiscoveryOptionalGroups []string `json:"discoveryOptionalGroups"`
}

var (
	builtinKindExcludes      []string
	builtinNamespaceExcludes []string
	discoveryOptionalGroups  map[string]bool
)

func init() {
	var d builtinDefaults
	if err := yaml.Unmarshal(defaultsYAML, &d); err != nil {
		panic("config: invalid embedded defaults.yaml: " + err.Error())
	}
	builtinKindExcludes = d.KindExclude
	builtinNamespaceExcludes = d.NamespaceExclude
	discoveryOptionalGroups = make(map[string]bool, len(d.DiscoveryOptionalGroups))
	for _, g := range d.DiscoveryOptionalGroups {
		discoveryOptionalGroups[g] = true
	}
}

// IsBuiltinKindExclude reports whether ref is one of the built-in
// scope.kinds.exclude entries, which apply on every run and so are never
// reported as matching nothing.
func IsBuiltinKindExclude(ref string) bool {
	for _, b := range builtinKindExcludes {
		if b == ref {
			return true
		}
	}
	return false
}

// DiscoveryOptional reports whether a failed discovery of API group may be
// ignored: a built-in list of groups (the aggregated metrics APIs) whose types
// are never scanned, so losing them never makes a run partial.
func DiscoveryOptional(group string) bool {
	return discoveryOptionalGroups[group]
}

// Default returns the built-in default configuration: current-context only,
// built-in scope exclusions (ADR-0003), and standard scoring thresholds
// (ADR-0002).
func Default() Config {
	return Config{
		Clusters: ClustersConfig{
			ProbeTimeout:   5 * time.Second,
			RequestTimeout: 30 * time.Second,
		},
		Scope: ScopeConfig{
			IncludeClusterScoped: false,
			Namespaces: NamespaceScope{
				Exclude: append([]string(nil), builtinNamespaceExcludes...),
			},
			Kinds: KindScope{
				Exclude: append([]string(nil), builtinKindExcludes...),
			},
		},
		Analysis: AnalysisConfig{
			CommonThreshold:          0.6,
			RareThreshold:            0.34,
			MinScore:                 40,
			ArgoInstanceLabelKey:     "app.kubernetes.io/instance",
			ReportMissingFromCluster: true,
		},
		Content: ContentConfig{
			Enabled: false,
			Kinds:   []string{"apps/Deployment", "ConfigMap", "networking.k8s.io/Ingress"},
		},
		Report: ReportConfig{
			Format: FormatTable,
			FailOn: FailOnNone,
		},
	}
}

// mergeDedup returns base followed by any elements of extra not already in
// base, preserving order and removing duplicates.
func mergeDedup(base, extra []string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, v := range base {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, v := range extra {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
