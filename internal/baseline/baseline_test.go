package baseline_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-shift-coding/kube-outlier/internal/baseline"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
)

var testNow = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func testScope() config.ScopeConfig {
	return config.ScopeConfig{
		IncludeClusterScoped: false,
		Namespaces: config.NamespaceScope{
			Exclude:         []string{"kube-system", "kube-public"},
			ExcludePatterns: []string{"pr-\\d+"},
		},
		Kinds: config.KindScope{Exclude: []string{"Event", "Pod"}},
	}
}

func testBaseline() *baseline.Baseline {
	entries := []baseline.Entry{
		{
			Key:              baseline.ResourceRef{Kind: "Secret", Namespace: "default", Name: "legacy-cert"},
			ClusterCount:     3,
			Ratio:            1.0,
			OwnershipSummary: map[ownership.Owner]int{ownership.OwnerUnknown: 3},
		},
		{
			Key:              baseline.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"},
			ClusterCount:     2,
			Ratio:            2.0 / 3,
			OwnershipSummary: map[ownership.Owner]int{ownership.OwnerHelm: 2},
		},
	}
	return baseline.New("v0.1.0-test", testNow, []string{"c3", "c1", "c2"},
		baseline.FingerprintScope(testScope()), entries, nil)
}

func TestFingerprint_DeterministicAndOrderInsensitive(t *testing.T) {
	a := baseline.FingerprintScope(testScope())

	shuffled := testScope()
	shuffled.Namespaces.Exclude = []string{"kube-public", "kube-system"}
	shuffled.Kinds.Exclude = []string{"Pod", "Event"}
	b := baseline.FingerprintScope(shuffled)

	if a.Hash == "" || a.Hash != b.Hash {
		t.Errorf("hashes differ for permuted lists: %q vs %q", a.Hash, b.Hash)
	}

	changed := testScope()
	changed.IncludeClusterScoped = true
	if c := baseline.FingerprintScope(changed); c.Hash == a.Hash {
		t.Error("changing includeClusterScoped must change the hash")
	}
	changed = testScope()
	changed.Kinds.Exclude = append(changed.Kinds.Exclude, "apps/StatefulSet")
	if c := baseline.FingerprintScope(changed); c.Hash == a.Hash {
		t.Error("adding a kind exclusion must change the hash")
	}
}

// An unrestricted scope must hash exactly as before namespaceInclude existed
// (so existing baselines and snapshots stay valid); restricting it must not.
func TestFingerprint_NamespaceInclude(t *testing.T) {
	a := baseline.FingerprintScope(testScope())
	if a.NamespaceInclude != nil {
		t.Errorf("unrestricted scope: NamespaceInclude = %v, want nil", a.NamespaceInclude)
	}

	restricted := testScope()
	restricted.Namespaces.Include = []string{"team-b", "team-a", "team-b"}
	b := baseline.FingerprintScope(restricted)
	if !reflect.DeepEqual(b.NamespaceInclude, []string{"team-a", "team-b"}) {
		t.Errorf("NamespaceInclude = %v, want sorted and deduped", b.NamespaceInclude)
	}
	if b.Hash == a.Hash {
		t.Error("adding a namespace include must change the hash")
	}
	if joined := strings.Join(baseline.Diff(a, b), "\n"); !strings.Contains(joined, "namespaceInclude: [+team-a +team-b]") {
		t.Errorf("diff missing namespaceInclude line:\n%s", joined)
	}
}

func TestFingerprint_Diff(t *testing.T) {
	a := baseline.FingerprintScope(testScope())
	changed := testScope()
	changed.IncludeClusterScoped = true
	changed.Kinds.Exclude = []string{"Event"} // Pod removed
	b := baseline.FingerprintScope(changed)

	lines := baseline.Diff(a, b)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "includeClusterScoped: baseline=false scan=true") {
		t.Errorf("diff missing includeClusterScoped line:\n%s", joined)
	}
	if !strings.Contains(joined, "kindExclude") || !strings.Contains(joined, "-Pod") {
		t.Errorf("diff missing kindExclude -Pod:\n%s", joined)
	}
	if got := baseline.Diff(a, a); got != nil {
		t.Errorf("identical fingerprints must diff empty, got %v", got)
	}
}

func TestRoundTrip(t *testing.T) {
	b := testBaseline()

	for _, asJSON := range []bool{false, true} {
		var buf bytes.Buffer
		if err := b.Write(&buf, asJSON); err != nil {
			t.Fatalf("Write(asJSON=%v): %v", asJSON, err)
		}
		path := filepath.Join(t.TempDir(), "b.yaml")
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := baseline.Load(path)
		if err != nil {
			t.Fatalf("Load(asJSON=%v): %v", asJSON, err)
		}
		if !reflect.DeepEqual(got, b) {
			t.Errorf("round-trip mismatch (asJSON=%v):\ngot  %+v\nwant %+v", asJSON, got, b)
		}
	}

	// Regeneration from identical inputs is byte-identical (stable sorts).
	var one, two bytes.Buffer
	if err := testBaseline().Write(&one, false); err != nil {
		t.Fatal(err)
	}
	if err := testBaseline().Write(&two, false); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one.Bytes(), two.Bytes()) {
		t.Error("two generations from the same inputs must be byte-identical")
	}

	// Entries sorted by Key.String(): "Secret default/legacy-cert" < "apps/Deployment ...".
	if b.Entries[0].Key.Kind != "Secret" {
		t.Errorf("entries not sorted by key string: first = %+v", b.Entries[0].Key)
	}
	// Provenance cluster names sorted; SchemaVersion pinned.
	if !reflect.DeepEqual(b.Provenance.Clusters, []string{"c1", "c2", "c3"}) || b.Provenance.SchemaVersion != 1 {
		t.Errorf("provenance = %+v", b.Provenance)
	}
	// Structural secret safety: the artifact type has no field that could
	// carry a Secret's data; the serialized form is keys/counts/hashes only.
	var buf bytes.Buffer
	if err := b.Write(&buf, false); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"stringData", "last-applied"} {
		if strings.Contains(buf.String(), forbidden) {
			t.Errorf("artifact contains %q:\n%s", forbidden, buf.String())
		}
	}
}

func TestLoad_Validation(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "b.yaml")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	serialize := func(t *testing.T, b *baseline.Baseline) string {
		t.Helper()
		var buf bytes.Buffer
		if err := b.Write(&buf, false); err != nil {
			t.Fatal(err)
		}
		return write(t, buf.String())
	}

	if _, err := baseline.Load(write(t, ":\tnot yaml")); err == nil {
		t.Error("unparseable file must error")
	}
	if _, err := baseline.Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("missing file must error")
	}

	newer := testBaseline()
	newer.Provenance.SchemaVersion = 2
	if _, err := baseline.Load(serialize(t, newer)); err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Errorf("schemaVersion 2 must error with upgrade hint, got %v", err)
	}

	noReason := testBaseline()
	noReason.Exceptions = []baseline.ApprovedException{
		{Key: baseline.ResourceRef{Kind: "ConfigMap", Namespace: "d", Name: "x"}, ApprovedBy: "someone"},
	}
	if _, err := baseline.Load(serialize(t, noReason)); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Errorf("exception without reason must error, got %v", err)
	}

	noApprover := testBaseline()
	noApprover.Exceptions = []baseline.ApprovedException{
		{Key: baseline.ResourceRef{Kind: "ConfigMap", Namespace: "d", Name: "x"}, Reason: "known"},
	}
	if _, err := baseline.Load(serialize(t, noApprover)); err == nil || !strings.Contains(err.Error(), "approvedBy") {
		t.Errorf("exception without approvedBy must error, got %v", err)
	}

	badEntry := testBaseline()
	badEntry.Entries[0].Key.Name = ""
	if _, err := baseline.Load(serialize(t, badEntry)); err == nil {
		t.Error("entry without name must error")
	}

	// expires takes an RFC 3339 time or a plain date (midnight UTC); anything
	// else names the exception and the accepted forms.
	withExpires := func(expires string) string {
		var buf bytes.Buffer
		if err := testBaseline().Write(&buf, false); err != nil {
			t.Fatal(err)
		}
		return write(t, "exceptions:\n  - resource: ConfigMap d/x\n    reason: known\n    approvedBy: someone\n    expires: "+expires+"\n"+buf.String())
	}
	for _, expires := range []string{"2027-01-01", `"2027-01-01"`, `"2027-01-01T00:00:00Z"`, "2027-01-01T00:00:00Z"} {
		b, err := baseline.Load(withExpires(expires))
		if err != nil {
			t.Errorf("expires %s: %v", expires, err)
			continue
		}
		if got := b.Exceptions[0].Expires; got == nil || !got.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("expires %s: got %v, want 2027-01-01 00:00 UTC", expires, got)
		}
	}
	if _, err := baseline.Load(withExpires("01/01/2027")); err == nil ||
		!strings.Contains(err.Error(), "exceptions[0] (ConfigMap d/x): expires: want a date such as 2027-01-01") {
		t.Errorf("bad expires must name the exception and the accepted forms, got %v", err)
	}

	// toolVersion skew alone is informational, never an error.
	skew := testBaseline()
	skew.Provenance.ToolVersion = "v9.9.9"
	if _, err := baseline.Load(serialize(t, skew)); err != nil {
		t.Errorf("toolVersion skew must not error, got %v", err)
	}
}

func TestActiveExceptions(t *testing.T) {
	past := testNow.AddDate(0, 0, -1)
	future := testNow.AddDate(0, 0, 1)
	keyActive := identity.ResourceKey{Kind: "ConfigMap", Namespace: "d", Name: "forever"}
	keyFuture := identity.ResourceKey{Kind: "ConfigMap", Namespace: "d", Name: "later"}
	keyExpired := identity.ResourceKey{Kind: "ConfigMap", Namespace: "d", Name: "old"}

	b := testBaseline()
	b.Exceptions = []baseline.ApprovedException{
		{Key: baseline.ResourceRef(keyActive), Reason: "r", ApprovedBy: "a"},
		{Key: baseline.ResourceRef(keyFuture), Reason: "r", ApprovedBy: "a", Expires: &future},
		{Key: baseline.ResourceRef(keyExpired), Reason: "r", ApprovedBy: "a", Expires: &past},
	}

	active, expired := b.ActiveExceptions(testNow)
	if len(active) != 2 || !reflect.DeepEqual(active[keyActive].Key.ResourceKey(), keyActive) || !reflect.DeepEqual(active[keyFuture].Key.ResourceKey(), keyFuture) {
		t.Errorf("active = %+v, want forever + later", active)
	}
	if len(expired) != 1 || !reflect.DeepEqual(expired[0].Key.ResourceKey(), keyExpired) {
		t.Errorf("expired = %+v, want old", expired)
	}
}
