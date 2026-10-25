package baseline

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/vibe-shift-coding/kube-outlier/internal/orderedyaml"
)

// Write emits the artifact as YAML (the committable default) or, when asJSON,
// as JSON with 2-space indent and a trailing newline — the identical
// structure and field order either way (ADR-0004).
func (b *Baseline) Write(w io.Writer, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(b)
	}
	raw, err := orderedyaml.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshalling baseline: %w", err)
	}
	_, err = w.Write(raw)
	return err
}

// Load reads and validates a baseline artifact. sigs.k8s.io/yaml parses both
// YAML and JSON. Every validation failure is fatal to the caller (exit 1):
// silently scanning against a corrupt or future-schema baseline is the risk
// ADR-0004 exists to prevent.
func Load(path string) (*Baseline, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("baseline %s: %w", path, err)
	}
	var rb rawBaseline
	if err := yaml.Unmarshal(raw, &rb); err != nil {
		return nil, fmt.Errorf("baseline %s: parsing: %w", path, err)
	}
	b, err := rb.baseline()
	if err != nil {
		return nil, fmt.Errorf("baseline %s: %w", path, err)
	}
	if err := b.validate(); err != nil {
		return nil, fmt.Errorf("baseline %s: %w", path, err)
	}
	return b, nil
}

// rawBaseline is Baseline as read from a file: the same fields, but with an
// exception's expires still a string, so it may be a plain date and a bad
// value can be reported with the exception it belongs to.
type rawBaseline struct {
	Provenance Provenance     `json:"provenance"`
	Exceptions []rawException `json:"exceptions,omitempty"`
	Entries    []Entry        `json:"entries"`
}

type rawException struct {
	Key        ResourceRef `json:"resource"`
	Reason     string      `json:"reason"`
	ApprovedBy string      `json:"approvedBy"`
	Expires    string      `json:"expires,omitempty"`
}

// expiresDate is the date-only form an exception's expires may take,
// meaning midnight UTC at the start of that day.
const expiresDate = "2006-01-02"

// baseline converts the file form, parsing each expires as an RFC 3339 time
// or a plain date.
func (rb rawBaseline) baseline() (*Baseline, error) {
	b := &Baseline{Provenance: rb.Provenance, Entries: rb.Entries}
	for i, e := range rb.Exceptions {
		ex := ApprovedException{Key: e.Key, Reason: e.Reason, ApprovedBy: e.ApprovedBy}
		if e.Expires != "" {
			t, err := time.Parse(time.RFC3339, e.Expires)
			if err != nil {
				if t, err = time.Parse(expiresDate, e.Expires); err != nil {
					return nil, fmt.Errorf("exceptions[%d] (%s): expires: want a date such as 2027-01-01 or an RFC 3339 time such as 2027-01-01T00:00:00Z, got %q", i, e.Key.String(), e.Expires)
				}
			}
			ex.Expires = &t
		}
		b.Exceptions = append(b.Exceptions, ex)
	}
	return b, nil
}

func (b *Baseline) validate() error {
	switch v := b.Provenance.SchemaVersion; {
	case v == SchemaVersion:
	case v > SchemaVersion:
		return fmt.Errorf("schemaVersion %d is newer than this build supports (%d) — upgrade kube-outlier", v, SchemaVersion)
	default:
		return fmt.Errorf("schemaVersion %d is not supported (want %d)", v, SchemaVersion)
	}
	if b.Provenance.ClusterCount < 1 {
		return fmt.Errorf("provenance.clusterCount %d: a baseline needs at least one contributing cluster", b.Provenance.ClusterCount)
	}
	if b.Provenance.Scope.Hash == "" {
		return fmt.Errorf("provenance.scope.hash is empty: scope fingerprint is mandatory")
	}
	for i, e := range b.Entries {
		if e.Key.Kind == "" || e.Key.Name == "" {
			return fmt.Errorf("entries[%d]: resource kind and name are required, got %+v", i, e.Key)
		}
	}
	for i, e := range b.Exceptions {
		if e.Reason == "" {
			return fmt.Errorf("exceptions[%d] (%s): reason is required", i, e.Key.String())
		}
		if e.ApprovedBy == "" {
			return fmt.Errorf("exceptions[%d] (%s): approvedBy is required", i, e.Key.String())
		}
	}
	return nil
}
