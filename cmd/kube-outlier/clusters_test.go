package main

import (
	"bytes"
	"strings"
	"testing"
)

// runCLI executes the root command with args and returns (exit code, stdout, stderr).
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	err := root.Execute()
	code, msg := exitCodeAndMessage(err)
	if msg != "" {
		stderr.WriteString(msg + "\n")
	}
	return code, stdout.String(), stderr.String()
}

func TestClusters_UnknownContext(t *testing.T) {
	code, _, stderr := runCLI(t, "clusters",
		"--contexts", "nope",
		"--kubeconfig", "../../testdata/kubeconfig/unreachable.yaml",
	)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "nope") {
		t.Errorf("stderr = %q, want it to mention %q", stderr, "nope")
	}
	if !strings.Contains(stderr, "unreachable") {
		t.Errorf("stderr = %q, want it to list available contexts", stderr)
	}
}

// The single-context unreachable.yaml fixture has exactly one (unreachable)
// context, so --all-contexts yields zero reachable clusters. Per
// docs/interfaces.md §3b (the frozen exit-code contract), zero
// reachable clusters is exit code 1, not 2.
func TestClusters_AllUnreachable_ExitsFatal(t *testing.T) {
	code, _, stderr := runCLI(t, "clusters",
		"--all-contexts",
		"--kubeconfig", "../../testdata/kubeconfig/unreachable.yaml",
		"--probe-timeout", "500ms",
	)
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (zero reachable clusters)", code)
	}
	if !strings.Contains(stderr, "no reachable clusters") || !strings.Contains(stderr, "\n  unreachable: ") {
		t.Errorf("stderr = %q, want the headline plus a per-cluster reason line", stderr)
	}
}

func TestClusters_JSONOutputShape(t *testing.T) {
	code, stdout, _ := runCLI(t, "clusters",
		"--contexts", "nope",
		"--kubeconfig", "../../testdata/kubeconfig/unreachable.yaml",
		"-o", "json",
	)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on a fatal resolve error", stdout)
	}
}
