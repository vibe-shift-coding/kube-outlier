// Package identity defines the fleet-wide resource identity model shared by
// every analysis layer: a version-agnostic key and the cross-cluster
// presence matrix built once fleet enrichment is active.
package identity

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ResourceKey is the fleet-wide identity of a resource. It is VERSION-AGNOSTIC:
// group+kind, never apiVersion, because served versions legitimately differ per cluster.
type ResourceKey struct {
	Group     string `json:"group"`     // "" for the core group
	Kind      string `json:"kind"`      // e.g. "ConfigMap" (canonical CamelCase kind, not resource plural)
	Namespace string `json:"namespace"` // "" for cluster-scoped resources
	Name      string `json:"name"`
}

// String renders "group/Kind namespace/name" with core group as bare Kind and
// cluster-scoped as "Kind name". Used for map keys, sorting, --why lookup and baseline files.
func (k ResourceKey) String() string {
	kind := k.Kind
	if k.Group != "" {
		kind = k.Group + "/" + k.Kind
	}
	if k.Namespace == "" {
		return kind + " " + k.Name
	}
	return kind + " " + k.Namespace + "/" + k.Name
}

// MarshalJSON writes the four fields plus "id", the String() form, so a JSON
// consumer can pass a resource straight to --why or a baseline exception.
// Reading ignores "id": the fields are the identity.
func (k ResourceKey) MarshalJSON() ([]byte, error) {
	type fields ResourceKey // no methods, so no recursion
	return json.Marshal(struct {
		fields
		ID string `json:"id"`
	}{fields(k), k.String()})
}

// ParseKey parses the String() syntax back into a ResourceKey:
// "<[group/]Kind> <[namespace/]name>", e.g. "ConfigMap default/debug-dump",
// "apps/Deployment shop/api" or "rbac.authorization.k8s.io/ClusterRole admin".
// The kind part splits on its first slash (core group has none); a name with
// no slash is cluster-scoped. It is the syntax --why and baseline files use.
func ParseKey(s string) (ResourceKey, error) {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return ResourceKey{}, fmt.Errorf(`want "<[group/]Kind> <[namespace/]name>", e.g. "ConfigMap default/debug-dump" or "apps/Deployment shop/api"; got %q`, s)
	}
	var k ResourceKey
	if group, kind, found := strings.Cut(fields[0], "/"); found {
		k.Group, k.Kind = group, kind
	} else {
		k.Kind = fields[0]
	}
	if k.Kind == "" || strings.Contains(k.Kind, "/") {
		return ResourceKey{}, fmt.Errorf("%q: kind must be \"Kind\" or \"group/Kind\"", fields[0])
	}
	if ns, name, found := strings.Cut(fields[1], "/"); found {
		k.Namespace, k.Name = ns, name
		if ns == "" {
			return ResourceKey{}, fmt.Errorf("%q: empty namespace", fields[1])
		}
	} else {
		k.Name = fields[1]
	}
	if k.Name == "" || strings.Contains(k.Name, "/") {
		return ResourceKey{}, fmt.Errorf("%q: name must be \"name\" or \"namespace/name\"", fields[1])
	}
	return k, nil
}

// PresenceMatrix maps identities to the reachable clusters they exist on. Built only
// when N >= 2. Cluster names are connect.ClusterRef.Name values.
type PresenceMatrix struct {
	Clusters []string                        // all reachable clusters, stable sorted order; len == N
	ByKey    map[ResourceKey]map[string]bool // key -> set of cluster names where present
}

// NewPresenceMatrix creates a PresenceMatrix over the given reachable cluster names.
// The cluster list is copied and sorted so Clusters has a stable order.
func NewPresenceMatrix(clusters []string) *PresenceMatrix {
	sorted := make([]string, len(clusters))
	copy(sorted, clusters)
	sort.Strings(sorted)
	return &PresenceMatrix{
		Clusters: sorted,
		ByKey:    make(map[ResourceKey]map[string]bool),
	}
}

// Add records that resource k is present on cluster.
func (m *PresenceMatrix) Add(k ResourceKey, cluster string) {
	set, ok := m.ByKey[k]
	if !ok {
		set = make(map[string]bool)
		m.ByKey[k] = set
	}
	set[cluster] = true
}

// PresentOn returns the sorted list of clusters where k is present.
func (m *PresenceMatrix) PresentOn(k ResourceKey) []string {
	set := m.ByKey[k]
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// MissingFrom returns the sorted list of clusters (from m.Clusters) where k is absent.
func (m *PresenceMatrix) MissingFrom(k ResourceKey) []string {
	set := m.ByKey[k]
	out := make([]string, 0, len(m.Clusters))
	for _, c := range m.Clusters {
		if !set[c] {
			out = append(out, c)
		}
	}
	return out
}

// Ratio returns |PresentOn(k)| / len(Clusters), or 0 if k is unknown.
func (m *PresenceMatrix) Ratio(k ResourceKey) float64 {
	if len(m.Clusters) == 0 {
		return 0
	}
	set, ok := m.ByKey[k]
	if !ok {
		return 0
	}
	return float64(len(set)) / float64(len(m.Clusters))
}
