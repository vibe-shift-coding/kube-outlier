package baseline_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-shift-coding/kube-outlier/internal/baseline"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
)

// The file writes resources as identity strings and puts the hand-edited
// exceptions above the generated entries.
func TestWrite_IdentityStringsAndOrder(t *testing.T) {
	key := identity.ResourceKey{Group: "rbac.authorization.k8s.io", Kind: "ClusterRoleBinding", Name: "debug"}
	b := baseline.New("v0.1.0", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), []string{"a"},
		baseline.ScopeFingerprint{Hash: "h"},
		[]baseline.Entry{{Key: baseline.ResourceRef(key), ClusterCount: 1, Ratio: 1}},
		[]baseline.ApprovedException{{Key: baseline.ResourceRef(key), Reason: "r", ApprovedBy: "me"}})

	var buf bytes.Buffer
	if err := b.Write(&buf, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "resource: rbac.authorization.k8s.io/ClusterRoleBinding debug\n") {
		t.Errorf("want identity-string resources, got:\n%s", out)
	}
	p, x, e := strings.Index(out, "provenance:"), strings.Index(out, "exceptions:"), strings.Index(out, "entries:")
	if p != 0 || x < p || e < x {
		t.Errorf("want provenance, exceptions, entries in that order, got:\n%s", out)
	}
}

// Hand-written exceptions may use either the identity string or the object
// form; a malformed string is a clear load error.
func TestLoad_ExceptionResourceForms(t *testing.T) {
	const head = "provenance:\n  schemaVersion: 1\n  clusterCount: 2\n  scope:\n    hash: h\nentries: []\nexceptions:\n"
	dir := t.TempDir()

	good := filepath.Join(dir, "good.yaml")
	writeFile(t, good, head+
		"  - resource: ConfigMap default/known\n    reason: r\n    approvedBy: a\n"+
		"  - resource: {group: \"\", kind: Secret, namespace: default, name: legacy}\n    reason: r\n    approvedBy: a\n")
	b, err := baseline.Load(good)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []identity.ResourceKey{
		{Kind: "ConfigMap", Namespace: "default", Name: "known"},
		{Kind: "Secret", Namespace: "default", Name: "legacy"},
	}
	for i, w := range want {
		if b.Exceptions[i].Key.ResourceKey() != w {
			t.Errorf("exceptions[%d] = %+v, want %+v", i, b.Exceptions[i].Key, w)
		}
	}

	bad := filepath.Join(dir, "bad.yaml")
	writeFile(t, bad, head+"  - resource: ConfigMap\n    reason: r\n    approvedBy: a\n")
	if _, err := baseline.Load(bad); err == nil || !strings.Contains(err.Error(), "resource") {
		t.Errorf("want a resource parse error, got %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
