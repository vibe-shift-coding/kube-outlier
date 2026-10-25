package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
)

const defaultEnvPrefix = "KUBE_OUTLIER_"

// configDocsURL is where error messages send users for the list of settings:
// a binary installed with go install has no docs directory next to it.
const configDocsURL = "https://github.com/vibe-shift-coding/kube-outlier/blob/main/docs/configuration.md"

// LoadOptions controls where Load reads configuration from.
type LoadOptions struct {
	File      string         // from --config; "" = search default locations (ADR-0003)
	Flags     *pflag.FlagSet // may be nil in tests
	EnvPrefix string
}

// flagKeyMap maps the user-facing persistent flag names to their dotted koanf
// config path, since flag names ("--output") intentionally differ from the
// nested config schema ("report.format").
var flagKeyMap = map[string]string{
	"contexts":               "clusters.contexts",
	"all-contexts":           "clusters.allContexts",
	"kubeconfig":             "clusters.kubeconfigs",
	"probe-timeout":          "clusters.probeTimeout",
	"request-timeout":        "clusters.requestTimeout",
	"min-score":              "analysis.minScore",
	"output":                 "report.format",
	"fail-on":                "report.failOn",
	"allow-partial":          "report.allowPartial",
	"include-cluster-scoped": "scope.includeClusterScoped",
	"namespaces":             "scope.namespaces.include",
}

// Load builds a Config by layering, highest priority last: built-in defaults,
// config file, environment variables, then explicitly-set CLI flags. A key in
// the file or an environment variable under the prefix that names no setting
// is an error, so a typo can never silently fall back to a default.
func Load(opts LoadOptions) (*Config, error) {
	k := koanf.New(".")

	if err := k.Load(structs.Provider(Default(), "koanf"), nil); err != nil {
		return nil, fmt.Errorf("loading built-in defaults: %w", err)
	}

	path := opts.File
	explicit := path != ""
	if explicit {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("config file %s: %w", path, err)
		}
	} else {
		path = DefaultConfigPath()
	}
	canonical := canonicalKeys()
	if path != "" {
		fk := koanf.New(".")
		if err := fk.Load(file.Provider(path), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("parsing config file %s: %w", path, err)
		}
		if unknown := unknownKeys(fk.Keys(), canonical); len(unknown) > 0 {
			return nil, fmt.Errorf("config file %s: unknown setting(s) %s (see %s)", path, strings.Join(unknown, ", "), configDocsURL)
		}
		// Re-key onto the canonical camelCase spelling: a differently-cased
		// key would otherwise sit beside the default and lose at unmarshal.
		flat := make(map[string]interface{})
		for key, v := range fk.All() {
			if c, ok := canonical[strings.ToLower(key)]; ok {
				flat[c] = v
			}
		}
		if err := k.Load(confmap.Provider(flat, "."), nil); err != nil {
			return nil, fmt.Errorf("merging config file %s: %w", path, err)
		}
	}

	envPrefix := opts.EnvPrefix
	if envPrefix == "" {
		envPrefix = defaultEnvPrefix
	}
	// Env var names are case-insensitive, but koanf keys are camelCase and the
	// defaults/file layers already hold the canonical spelling; an env key in
	// another case would sit beside it and lose at unmarshal time. Map each
	// env key onto its canonical config path.
	var unknownEnv []string
	envProvider := env.ProviderWithValue(envPrefix, ".", func(s string, v string) (string, interface{}) {
		key := strings.ToLower(strings.TrimPrefix(s, envPrefix))
		key = strings.ReplaceAll(key, "__", ".")
		c, ok := canonical[key]
		if !ok {
			unknownEnv = append(unknownEnv, s)
			return "", nil // koanf skips empty keys
		}
		return c, v
	})
	if err := k.Load(envProvider, nil); err != nil {
		return nil, fmt.Errorf("loading environment: %w", err)
	}
	if len(unknownEnv) > 0 {
		sort.Strings(unknownEnv)
		return nil, fmt.Errorf("unknown environment variable(s) %s: %s<SECTION>__<KEY> must name a setting (see %s)", strings.Join(unknownEnv, ", "), envPrefix, configDocsURL)
	}

	if overrides := flagOverrides(opts.Flags); len(overrides) > 0 {
		if err := k.Load(confmap.Provider(overrides, "."), nil); err != nil {
			return nil, fmt.Errorf("loading flags: %w", err)
		}
	}

	var cfg Config
	err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		Tag: "koanf",
		DecoderConfig: &mapstructure.DecoderConfig{
			Result:           &cfg,
			WeaklyTypedInput: true,
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
			),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	cfg.Scope.Kinds.Exclude = mergeDedup(builtinKindExcludes, cfg.Scope.Kinds.Exclude)
	cfg.Scope.Namespaces.Exclude = mergeDedup(builtinNamespaceExcludes, cfg.Scope.Namespaces.Exclude)

	return &cfg, nil
}

// unknownKeys returns, sorted, the dotted keys that name no setting. Matching
// is case-insensitive, like the decoder's.
func unknownKeys(keys []string, canonical map[string]string) []string {
	var out []string
	for _, key := range keys {
		lower := strings.ToLower(key)
		if _, ok := canonical[lower]; ok || isSection(lower, canonical) {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// isSection reports whether lower is a section path such as "analysis" (an
// empty "analysis:" block in a file flattens to that key).
func isSection(lower string, canonical map[string]string) bool {
	for c := range canonical {
		if strings.HasPrefix(c, lower+".") {
			return true
		}
	}
	return false
}

// canonicalKeys maps every lowercased dotted leaf setting path (e.g.
// "analysis.minscore") to its canonical koanf spelling ("analysis.minScore"),
// derived from the Config struct's koanf tags. Section paths ("analysis") are
// not settings and are left out.
func canonicalKeys() map[string]string {
	out := map[string]string{}
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("koanf")
			if tag == "" {
				continue
			}
			path := tag
			if prefix != "" {
				path = prefix + "." + tag
			}
			if f.Type.Kind() == reflect.Struct {
				walk(f.Type, path)
				continue
			}
			out[strings.ToLower(path)] = path
		}
	}
	walk(reflect.TypeOf(Config{}), "")
	return out
}

// DefaultConfigPath returns the first existing candidate from the default
// search locations (ADR-0003): ./.kube-outlier.yaml, then
// $XDG_CONFIG_HOME/kube-outlier/config.yaml (~/.config when unset). It returns
// "" when none exists.
func DefaultConfigPath() string {
	candidates := []string{".kube-outlier.yaml"}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "kube-outlier", "config.yaml"))
	} else if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "kube-outlier", "config.yaml"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// flagOverrides returns the dotted-path config values for every persistent
// flag the user explicitly set (Flags.Changed), so unset flags never clobber
// values from lower-priority layers (env, file, defaults).
func flagOverrides(flags *pflag.FlagSet) map[string]interface{} {
	m := map[string]interface{}{}
	if flags == nil {
		return m
	}
	for name, key := range flagKeyMap {
		if !flags.Changed(name) {
			continue
		}
		f := flags.Lookup(name)
		if f == nil {
			continue
		}
		switch f.Value.Type() {
		case "stringSlice":
			v, _ := flags.GetStringSlice(name)
			m[key] = v
		case "stringArray":
			v, _ := flags.GetStringArray(name)
			m[key] = v
		case "bool":
			v, _ := flags.GetBool(name)
			m[key] = v
		case "duration":
			v, _ := flags.GetDuration(name)
			m[key] = v
		default:
			m[key] = f.Value.String()
		}
	}
	return m
}
