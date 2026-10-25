// Package baseline implements the portable fleet fingerprint: a
// committable summary of what "normal" looks like across a fleet, so a later
// scan can compare a single cluster against it without re-scanning peers.
// Types are frozen in docs/interfaces.md §2.7; format and validation rules in
// ADR-0004. A baseline never contains manifests, spec bodies, or secret
// values — presence counts and ownership tallies only.
package baseline

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
)

// SchemaVersion is the baseline artifact schema this build reads and writes.
const SchemaVersion = 1

// ScopeFingerprint captures exactly the scan scope that produced a baseline.
// Hash = sha256 hex of the canonical JSON (sorted keys, no whitespace) of the
// other fields. A scan against a baseline MUST compare fingerprints (ADR-0004).
type ScopeFingerprint struct {
	IncludeClusterScoped     bool     `json:"includeClusterScoped"`
	NamespaceInclude         []string `json:"namespaceInclude,omitempty"` // sorted, deduped; omitted (and hash unchanged) when not restricting
	NamespaceExclude         []string `json:"namespaceExclude"`           // sorted
	NamespaceExcludePatterns []string `json:"namespaceExcludePatterns"`   // sorted
	KindExclude              []string `json:"kindExclude"`                // canonical KindRef syntax, sorted, built-ins merged in
	KindInclude              []string `json:"kindInclude"`                // sorted
	Hash                     string   `json:"hash"`
}

// Provenance is the mandatory baseline header.
type Provenance struct {
	SchemaVersion int              `json:"schemaVersion"` // 1
	ToolVersion   string           `json:"toolVersion"`
	GeneratedAt   time.Time        `json:"generatedAt"`
	ClusterCount  int              `json:"clusterCount"` // contributing (reachable) clusters
	Clusters      []string         `json:"clusters"`     // contributing cluster names, sorted
	Scope         ScopeFingerprint `json:"scope"`
}

// Entry is one identity's fleet fingerprint. Ratio is exactly
// ClusterCount / Provenance.ClusterCount: kinds not cleanly listable on every
// contributing cluster are excluded from the baseline entirely (the
// clean-listing rule), so no per-entry denominator is needed. In the file,
// resource is the identity string ("apps/Deployment shop/api"), see
// ResourceRef.
type Entry struct {
	Key              ResourceRef             `json:"resource"`
	ClusterCount     int                     `json:"clusterCount"`
	Ratio            float64                 `json:"ratio"`
	OwnershipSummary map[ownership.Owner]int `json:"ownershipSummary"`
}

// ApprovedException marks an identity as a known-legitimate one-off,
// suppressing its findings in baseline-backed scans. Hand-maintained.
type ApprovedException struct {
	Key        ResourceRef `json:"resource"`
	Reason     string      `json:"reason"`            // required, non-empty
	ApprovedBy string      `json:"approvedBy"`        // required, non-empty
	Expires    *time.Time  `json:"expires,omitempty"` // nil = never
}

// Baseline is the complete artifact. Field order is file order: the
// hand-edited exceptions come before the (possibly long) generated entries.
type Baseline struct {
	Provenance Provenance          `json:"provenance"`
	Exceptions []ApprovedException `json:"exceptions,omitempty"`
	Entries    []Entry             `json:"entries"` // sorted by Key.String()
}

// ResourceRef is a ResourceKey as it appears in a baseline file: the identity
// string that --why takes ("ConfigMap default/debug-dump",
// "rbac.authorization.k8s.io/ClusterRole admin"). Reading also accepts the
// object form {group, kind, namespace, name}.
type ResourceRef identity.ResourceKey

// MarshalJSON writes the identity string.
func (r ResourceRef) MarshalJSON() ([]byte, error) {
	return json.Marshal(identity.ResourceKey(r).String())
}

// UnmarshalJSON reads the identity string or the {group, kind, namespace,
// name} object.
func (r *ResourceRef) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		k, err := identity.ParseKey(s)
		if err != nil {
			return fmt.Errorf("resource: %w", err)
		}
		*r = ResourceRef(k)
		return nil
	}
	var k identity.ResourceKey
	if err := json.Unmarshal(data, &k); err != nil {
		return fmt.Errorf(`resource: want "<[group/]Kind> <[namespace/]name>" or {group, kind, namespace, name}: %w`, err)
	}
	*r = ResourceRef(k)
	return nil
}

// ResourceKey returns the identity.
func (r ResourceRef) ResourceKey() identity.ResourceKey { return identity.ResourceKey(r) }

// String renders the identity string.
func (r ResourceRef) String() string { return identity.ResourceKey(r).String() }

// ActiveExceptions splits the exceptions block at now: a lookup map of the
// active ones keyed by ResourceKey, plus the expired ones so the caller can
// report each once on stderr.
func (b *Baseline) ActiveExceptions(now time.Time) (map[identity.ResourceKey]ApprovedException, []ApprovedException) {
	active := make(map[identity.ResourceKey]ApprovedException, len(b.Exceptions))
	var expired []ApprovedException
	for _, e := range b.Exceptions {
		if e.Expires != nil && e.Expires.Before(now) {
			expired = append(expired, e)
			continue
		}
		active[e.Key.ResourceKey()] = e
	}
	return active, expired
}
