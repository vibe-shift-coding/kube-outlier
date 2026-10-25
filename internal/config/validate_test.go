package config

import (
	"strings"
	"testing"
)

func TestParseKindRef(t *testing.T) {
	cases := []struct {
		in      string
		want    KindRef
		wantErr bool
	}{
		{in: "ConfigMap", want: KindRef{Kind: "ConfigMap"}},
		{in: "apps/Deployment", want: KindRef{Group: "apps", Kind: "Deployment"}},
		{in: "networking.k8s.io/Ingress", want: KindRef{Group: "networking.k8s.io", Kind: "Ingress"}},
		{in: "v1/Event", want: KindRef{Group: "v1", Kind: "Event"}},
		{in: "apps/", wantErr: true},
		{in: "", wantErr: true},
		{in: "a/b/c", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseKindRef(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseKindRef(%q) = %v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseKindRef(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseKindRef(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func validConfig() Config {
	cfg := Default()
	return cfg
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:   "default is valid",
			mutate: func(c *Config) {},
		},
		{
			name:    "bad sensitive kind",
			mutate:  func(c *Config) { c.Sensitive.Kinds = []string{"a/b/c"} },
			wantErr: "sensitive.kinds",
		},
		{
			name:    "commonThreshold zero",
			mutate:  func(c *Config) { c.Analysis.CommonThreshold = 0 },
			wantErr: "analysis.commonThreshold",
		},
		{
			name:    "commonThreshold too big",
			mutate:  func(c *Config) { c.Analysis.CommonThreshold = 1.2 },
			wantErr: "analysis.commonThreshold",
		},
		{
			name: "rareThreshold >= commonThreshold",
			mutate: func(c *Config) {
				c.Analysis.CommonThreshold = 0.5
				c.Analysis.RareThreshold = 0.5
			},
			wantErr: "analysis.rareThreshold",
		},
		{
			name:    "minScore out of range",
			mutate:  func(c *Config) { c.Analysis.MinScore = 101 },
			wantErr: "analysis.minScore",
		},
		{
			name:    "bad kind ref",
			mutate:  func(c *Config) { c.Scope.Kinds.Exclude = append(c.Scope.Kinds.Exclude, "a/b/c") },
			wantErr: "scope.kinds.exclude",
		},
		{
			name:    "empty kind ref",
			mutate:  func(c *Config) { c.Scope.Kinds.Exclude = append(c.Scope.Kinds.Exclude, "") },
			wantErr: "scope.kinds.exclude",
		},
		{
			name:    "bad namespace pattern",
			mutate:  func(c *Config) { c.Scope.Namespaces.ExcludePatterns = append(c.Scope.Namespaces.ExcludePatterns, "(") },
			wantErr: "scope.namespaces.excludePatterns",
		},
		{
			name:    "bad namespace include",
			mutate:  func(c *Config) { c.Scope.Namespaces.Include = []string{"team-a", "Team_B"} },
			wantErr: "scope.namespaces.include",
		},
		{
			name:    "bad format",
			mutate:  func(c *Config) { c.Report.Format = "xml" },
			wantErr: "report.format",
		},
		{
			name:    "bad failOn",
			mutate:  func(c *Config) { c.Report.FailOn = "sometimes" },
			wantErr: "report.failOn",
		},
		{
			name: "contexts and allContexts both set",
			mutate: func(c *Config) {
				c.Clusters.Contexts = []string{"a"}
				c.Clusters.AllContexts = true
			},
			wantErr: "clusters.contexts",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// Errors for values that usually come from a flag name the flag as well as
// the key, and list what is allowed.
func TestValidate_ErrorsNameFlagAndAllowedValues(t *testing.T) {
	c := Default()
	c.Report.Format = "wide"
	c.Report.FailOn = "bogus"
	err := c.Validate()
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"-o/--output", "use table, json, yaml or markdown", "--fail-on", "use none, inventory, content or any"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// The core Secret is always sensitive; sensitive.kinds adds to it and can't
// remove it.
func TestSensitiveKindSet(t *testing.T) {
	c := Default()
	if got := c.SensitiveKindSet(); len(got) != 1 || !got[KindRef{Kind: "Secret"}] {
		t.Errorf("default set = %v, want only Secret", got)
	}

	c.Sensitive.Kinds = []string{"generators.external-secrets.io/Fake", "a/b/c"}
	got := c.SensitiveKindSet()
	if !got[KindRef{Kind: "Secret"}] || !got[KindRef{Group: "generators.external-secrets.io", Kind: "Fake"}] {
		t.Errorf("set = %v, want Secret and the listed kind", got)
	}
	if len(got) != 2 {
		t.Errorf("set = %v, want the invalid entry skipped", got)
	}
}
