package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func TestLoad_NoFileAnywhere(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	cfg, err := Load(LoadOptions{})
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	want := Default()
	if cfg.Report.Format != want.Report.Format || cfg.Analysis.CommonThreshold != want.Analysis.CommonThreshold {
		t.Errorf("Load() = %+v, want defaults %+v", cfg, want)
	}
}

func TestLoad_ExplicitMissingFile(t *testing.T) {
	_, err := Load(LoadOptions{File: "/no/such/file.yaml"})
	if err == nil {
		t.Fatal("Load() with missing explicit --config file: want error, got nil")
	}
}

func TestLoad_MissingDefaultLocationIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if _, err := Load(LoadOptions{}); err != nil {
		t.Fatalf("Load() with no default-location file: want no error, got %v", err)
	}
}

func TestLoad_Precedence(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgFile, "report:\n  format: json\n")

	// File alone.
	cfg, err := Load(LoadOptions{File: cfgFile})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Report.Format != FormatJSON {
		t.Errorf("file-only: Report.Format = %q, want json", cfg.Report.Format)
	}

	// Env overrides file.
	t.Setenv("KUBE_OUTLIER_REPORT__FORMAT", "yaml")
	cfg, err = Load(LoadOptions{File: cfgFile})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Report.Format != FormatYAML {
		t.Errorf("env-over-file: Report.Format = %q, want yaml", cfg.Report.Format)
	}

	// Flag overrides env and file.
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	registerFlagsForTest(flags)
	if err := flags.Parse([]string{"--output", "table"}); err != nil {
		t.Fatalf("flags.Parse: %v", err)
	}
	cfg, err = Load(LoadOptions{File: cfgFile, Flags: flags})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Report.Format != FormatTable {
		t.Errorf("flag-over-env-and-file: Report.Format = %q, want table", cfg.Report.Format)
	}
}

// Env var names are upper-case, so camelCase keys must still resolve: an env
// value has to beat both the built-in default and the config file.
func TestLoad_EnvCamelCaseKeys(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfgFile := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgFile, "analysis:\n  minScore: 50\n")

	t.Setenv("KUBE_OUTLIER_ANALYSIS__MINSCORE", "60")
	t.Setenv("KUBE_OUTLIER_REPORT__FAILON", "inventory")
	t.Setenv("KUBE_OUTLIER_SCOPE__INCLUDECLUSTERSCOPED", "true")

	for _, tc := range []struct {
		name string
		opts LoadOptions
	}{
		{"defaults only", LoadOptions{}},
		{"over config file", LoadOptions{File: cfgFile}},
	} {
		cfg, err := Load(tc.opts)
		if err != nil {
			t.Fatalf("%s: Load() error: %v", tc.name, err)
		}
		if cfg.Analysis.MinScore != 60 {
			t.Errorf("%s: Analysis.MinScore = %d, want 60 from env", tc.name, cfg.Analysis.MinScore)
		}
		if cfg.Report.FailOn != FailOnInventory {
			t.Errorf("%s: Report.FailOn = %q, want inventory from env", tc.name, cfg.Report.FailOn)
		}
		if !cfg.Scope.IncludeClusterScoped {
			t.Errorf("%s: Scope.IncludeClusterScoped = false, want true from env", tc.name)
		}
	}
}

func TestLoad_NamespacesFlagAndEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	t.Setenv("KUBE_OUTLIER_SCOPE__NAMESPACES__INCLUDE", "env-a,env-b")
	cfg, err := Load(LoadOptions{})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if want := []string{"env-a", "env-b"}; !equalStrings(cfg.Scope.Namespaces.Include, want) {
		t.Errorf("env: Scope.Namespaces.Include = %v, want %v", cfg.Scope.Namespaces.Include, want)
	}

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	registerFlagsForTest(flags)
	flags.StringSliceP("namespaces", "n", nil, "")
	if err := flags.Parse([]string{"-n", "team-a", "-n", "team-b,team-c"}); err != nil {
		t.Fatalf("flags.Parse: %v", err)
	}
	cfg, err = Load(LoadOptions{Flags: flags})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if want := []string{"team-a", "team-b", "team-c"}; !equalStrings(cfg.Scope.Namespaces.Include, want) {
		t.Errorf("flag over env: Scope.Namespaces.Include = %v, want %v", cfg.Scope.Namespaces.Include, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLoad_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgFile, "report:\n  format: [this is not valid yaml\n")

	_, err := Load(LoadOptions{File: cfgFile})
	if err == nil {
		t.Fatal("Load() with malformed YAML: want error, got nil")
	}
	if !strings.Contains(err.Error(), cfgFile) {
		t.Errorf("Load() error = %q, want it to mention path %q", err.Error(), cfgFile)
	}
}

func TestLoad_MergesKindAndNamespaceExcludes(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgFile, "scope:\n  kinds:\n    exclude: [\"apps/Deployment\"]\n  namespaces:\n    exclude: [\"velero\"]\n")

	cfg, err := Load(LoadOptions{File: cfgFile})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !containsStr(cfg.Scope.Kinds.Exclude, "apps/Deployment") || !containsStr(cfg.Scope.Kinds.Exclude, "Event") {
		t.Errorf("Scope.Kinds.Exclude = %v, want both user value and built-in", cfg.Scope.Kinds.Exclude)
	}
	if !containsStr(cfg.Scope.Namespaces.Exclude, "velero") || !containsStr(cfg.Scope.Namespaces.Exclude, "kube-system") {
		t.Errorf("Scope.Namespaces.Exclude = %v, want both user value and built-in", cfg.Scope.Namespaces.Exclude)
	}
}

func TestLoad_ProbeTimeoutFlag(t *testing.T) {
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	registerFlagsForTest(flags)
	if err := flags.Parse([]string{"--probe-timeout", "2s"}); err != nil {
		t.Fatalf("flags.Parse: %v", err)
	}
	cfg, err := Load(LoadOptions{Flags: flags})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Clusters.ProbeTimeout != 2*time.Second {
		t.Errorf("Clusters.ProbeTimeout = %v, want 2s", cfg.Clusters.ProbeTimeout)
	}
}

func registerFlagsForTest(flags *pflag.FlagSet) {
	flags.String("config", "", "")
	flags.StringSlice("contexts", nil, "")
	flags.Bool("all-contexts", false, "")
	flags.StringArray("kubeconfig", nil, "")
	flags.StringP("output", "o", "table", "")
	flags.Duration("probe-timeout", 5*time.Second, "")
	flags.CountP("verbose", "v", "")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// A typo or a removed setting in the file must fail loudly, never fall back
// to the default; an empty section block is fine.
func TestLoad_UnknownFileKeyIsAnError(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgFile, "analysis:\n  minScor: 10\ndrift:\n  enabled: true\nscope:\n")

	_, err := Load(LoadOptions{File: cfgFile})
	if err == nil {
		t.Fatal("Load() with unknown keys: want error, got nil")
	}
	for _, want := range []string{"analysis.minScor", "drift.enabled"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "scope") {
		t.Errorf("empty section reported as unknown: %v", err)
	}
}

func TestLoad_KeysMatchCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgFile, "analysis:\n  minscore: 55\n")

	cfg, err := Load(LoadOptions{File: cfgFile})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Analysis.MinScore != 55 {
		t.Errorf("MinScore = %d, want 55", cfg.Analysis.MinScore)
	}
}

func TestLoad_UnknownEnvVarIsAnError(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("KUBE_OUTLIER_ANALYSIS__MINSCOREX", "5")

	_, err := Load(LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "KUBE_OUTLIER_ANALYSIS__MINSCOREX") {
		t.Fatalf("Load() with unknown env var: want error naming it, got %v", err)
	}
}

func TestLoad_FlagsMapToSettings(t *testing.T) {
	t.Chdir(t.TempDir())
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.Int("min-score", 40, "")
	fs.Duration("request-timeout", 30*time.Second, "")
	if err := fs.Parse([]string{"--min-score=70", "--request-timeout=7s"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{Flags: fs})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Analysis.MinScore != 70 || cfg.Clusters.RequestTimeout != 7*time.Second {
		t.Errorf("got minScore %d, requestTimeout %v; want 70, 7s", cfg.Analysis.MinScore, cfg.Clusters.RequestTimeout)
	}
}
