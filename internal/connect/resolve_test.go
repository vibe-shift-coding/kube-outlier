package connect

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
)

func writeKubeconfig(t *testing.T, path string, contexts []string, currentContext string) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: Config\n")
	if currentContext != "" {
		sb.WriteString("current-context: " + currentContext + "\n")
	}
	sb.WriteString("clusters:\n")
	for _, c := range contexts {
		fmt.Fprintf(&sb, "  - name: %s\n    cluster:\n      server: https://127.0.0.1:1\n", c)
	}
	sb.WriteString("contexts:\n")
	for _, c := range contexts {
		fmt.Fprintf(&sb, "  - name: %s\n    context:\n      cluster: %s\n      user: %s\n", c, c, c)
	}
	sb.WriteString("users:\n")
	for _, c := range contexts {
		fmt.Fprintf(&sb, "  - name: %s\n    user: {}\n", c)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("writing kubeconfig %s: %v", path, err)
	}
}

func TestResolve_ContextsList(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig.yaml")
	writeKubeconfig(t, kc, []string{"a", "b", "c"}, "a")

	cfg := config.Default()
	cfg.Clusters.Kubeconfigs = []string{kc}
	cfg.Clusters.Contexts = []string{"a", "c"}

	refs, err := Resolve(&cfg)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if len(refs) != 2 || refs[0].Name != "a" || refs[1].Name != "c" {
		t.Errorf("Resolve() = %+v, want [a c] in order", refs)
	}
	for _, r := range refs {
		if r.Reachable {
			t.Errorf("ref %s: Reachable = true, want false before probing", r.Name)
		}
		if r.SkipReason != "" {
			t.Errorf("ref %s: SkipReason = %q, want empty before probing", r.Name, r.SkipReason)
		}
		if r.Kubeconfig != kc {
			t.Errorf("ref %s: Kubeconfig = %q, want %q", r.Name, r.Kubeconfig, kc)
		}
	}
}

func TestResolve_AllContexts(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig.yaml")
	writeKubeconfig(t, kc, []string{"a", "b", "c"}, "a")

	cfg := config.Default()
	cfg.Clusters.Kubeconfigs = []string{kc}
	cfg.Clusters.AllContexts = true

	refs, err := Resolve(&cfg)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "a,b,c" {
		t.Errorf("Resolve() names = %v, want [a b c]", names)
	}
}

func TestResolve_CurrentContextOnly(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig.yaml")
	writeKubeconfig(t, kc, []string{"a", "b"}, "b")

	cfg := config.Default()
	cfg.Clusters.Kubeconfigs = []string{kc}

	refs, err := Resolve(&cfg)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if len(refs) != 1 || refs[0].Name != "b" {
		t.Errorf("Resolve() = %+v, want just [b]", refs)
	}
}

func TestResolve_UnknownContext(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig.yaml")
	writeKubeconfig(t, kc, []string{"a", "b"}, "a")

	cfg := config.Default()
	cfg.Clusters.Kubeconfigs = []string{kc}
	cfg.Clusters.Contexts = []string{"nope"}

	_, err := Resolve(&cfg)
	if err == nil {
		t.Fatal("Resolve() with unknown context: want error, got nil")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("Resolve() error = %q, want it to mention %q", err.Error(), "nope")
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Errorf("Resolve() error = %q, want it to list available contexts", err.Error())
	}
}

// kubectl merges KUBECONFIG=a:b with the first file winning on a clashing
// context name; Resolve follows the same rule instead of failing.
func TestResolve_DuplicateContextAcrossFiles_FirstWins(t *testing.T) {
	dir := t.TempDir()
	kc1 := filepath.Join(dir, "one.yaml")
	kc2 := filepath.Join(dir, "two.yaml")
	writeKubeconfig(t, kc1, []string{"shared"}, "shared")
	writeKubeconfig(t, kc2, []string{"shared", "other"}, "shared")

	cfg := config.Default()
	cfg.Clusters.Kubeconfigs = []string{kc1, kc2}
	cfg.Clusters.AllContexts = true

	refs, err := Resolve(&cfg)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	// --all-contexts lists contexts by name.
	if len(refs) != 2 || refs[0].Name != "other" || refs[1].Name != "shared" || refs[1].Kubeconfig != kc1 {
		t.Fatalf("Resolve() = %+v, want other, then shared from %s", refs, kc1)
	}
}

// A kubeconfig named explicitly must be readable: a typo is an error, never
// a silently smaller fleet.
func TestResolve_MissingExplicitKubeconfig(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "real.yaml")
	writeKubeconfig(t, kc, []string{"a"}, "a")

	cfg := config.Default()
	cfg.Clusters.Kubeconfigs = []string{filepath.Join(dir, "typo.yaml"), kc}
	if _, err := Resolve(&cfg); err == nil || !strings.Contains(err.Error(), "typo.yaml") {
		t.Fatalf("Resolve() with a missing --kubeconfig: want an error naming it, got %v", err)
	}
}

// A config file gets no shell, so "~/" is expanded by the tool.
func TestResolve_ExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeKubeconfig(t, filepath.Join(home, "kc.yaml"), []string{"a"}, "a")

	cfg := config.Default()
	cfg.Clusters.Kubeconfigs = []string{"~/kc.yaml"}
	refs, err := Resolve(&cfg)
	if err != nil || len(refs) != 1 || refs[0].Kubeconfig != filepath.Join(home, "kc.yaml") {
		t.Fatalf("Resolve() with ~/kc.yaml = %+v, %v", refs, err)
	}
}

// As with KUBECONFIG=a:b, a context may use a user defined in another file.
func TestResolve_ContextUsesUserFromAnotherFile(t *testing.T) {
	dir := t.TempDir()
	ctxFile := filepath.Join(dir, "contexts.yaml")
	userFile := filepath.Join(dir, "users.yaml")
	writeFile := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(ctxFile, "apiVersion: v1\nkind: Config\nclusters:\n  - name: c\n    cluster: {server: https://127.0.0.1:1}\ncontexts:\n  - name: split\n    context: {cluster: c, user: alice}\n")
	writeFile(userFile, "apiVersion: v1\nkind: Config\nusers:\n  - name: alice\n    user: {token: t0ken}\n")

	cfg := config.Default()
	cfg.Clusters.Kubeconfigs = []string{ctxFile, userFile}
	cfg.Clusters.Contexts = []string{"split"}
	refs, err := Resolve(&cfg)
	if err != nil || len(refs) != 1 {
		t.Fatalf("Resolve() = %+v, %v", refs, err)
	}
	rc, err := restConfigFor(refs[0])
	if err != nil {
		t.Fatalf("restConfigFor: %v", err)
	}
	if rc.BearerToken != "t0ken" {
		t.Errorf("BearerToken = %q, want the user from %s", rc.BearerToken, userFile)
	}
}

func TestResolve_ContextListedTwice(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "one.yaml")
	writeKubeconfig(t, kc, []string{"a"}, "a")

	cfg := config.Default()
	cfg.Clusters.Kubeconfigs = []string{kc}
	cfg.Clusters.Contexts = []string{"a", "a"}

	if _, err := Resolve(&cfg); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("Resolve() with a repeated context: want error, got %v", err)
	}
}

func TestResolve_EmptyResult(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(kc, []byte("apiVersion: v1\nkind: Config\n"), 0o644); err != nil {
		t.Fatalf("writing kubeconfig: %v", err)
	}

	cfg := config.Default()
	cfg.Clusters.Kubeconfigs = []string{kc}

	_, err := Resolve(&cfg)
	if err == nil {
		t.Fatal("Resolve() with no contexts anywhere: want error, got nil")
	}
}

// A KUBECONFIG file that doesn't exist is named as such when no context is
// found anywhere, rather than looking like an empty file.
func TestResolve_NoContextsNamesMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	t.Setenv("KUBECONFIG", missing)
	cfg := config.Default()
	_, err := Resolve(&cfg)
	if err == nil || !strings.Contains(err.Error(), missing+" (not found)") {
		t.Errorf("Resolve() error = %v, want it to say %s was not found", err, missing)
	}
}
