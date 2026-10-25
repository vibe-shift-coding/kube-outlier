package main

import (
	"runtime/debug"
	"testing"
)

func TestVersion_PrintsToolVersion(t *testing.T) {
	code, stdout, _ := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if want := "kube-outlier " + version() + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestVersionFlag_MatchesSubcommand(t *testing.T) {
	_, sub, _ := runCLI(t, "version")
	code, flag, _ := runCLI(t, "--version")
	if code != 0 {
		t.Fatalf("--version exit code = %d, want 0", code)
	}
	if flag != sub {
		t.Errorf("--version printed %q, version subcommand printed %q; want identical output", flag, sub)
	}
}

func TestResolveVersion(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}
	noInfo := func() (*debug.BuildInfo, bool) { return nil, false }

	cases := []struct {
		name    string
		ldflags string
		build   func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"ldflags wins", "v1.2.3", info("v0.9.0"), "v1.2.3"},
		{"go install version", "dev", info("v0.1.0"), "v0.1.0"},
		{"local build", "dev", info("(devel)"), "dev"},
		{"empty module version", "dev", info(""), "dev"},
		{"no build info", "dev", noInfo, "dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.ldflags, tc.build); got != tc.want {
				t.Errorf("resolveVersion = %q, want %q", got, tc.want)
			}
		})
	}
}
