package baseline

import (
	"sort"
	"time"
)

// New assembles and normalizes a Baseline: entries sorted by Key.String() so
// regenerated baselines produce minimal VCS diffs (ADR-0004), provenance
// filled from the arguments, SchemaVersion pinned. Cluster names are sorted.
func New(toolVersion string, generatedAt time.Time, clusters []string, scope ScopeFingerprint, entries []Entry, exceptions []ApprovedException) *Baseline {
	sortedEntries := make([]Entry, len(entries))
	copy(sortedEntries, entries)
	sort.Slice(sortedEntries, func(i, j int) bool {
		return sortedEntries[i].Key.String() < sortedEntries[j].Key.String()
	})

	names := make([]string, len(clusters))
	copy(names, clusters)
	sort.Strings(names)

	return &Baseline{
		Provenance: Provenance{
			SchemaVersion: SchemaVersion,
			ToolVersion:   toolVersion,
			GeneratedAt:   generatedAt.UTC(),
			ClusterCount:  len(clusters),
			Clusters:      names,
			Scope:         scope,
		},
		Entries:    sortedEntries,
		Exceptions: exceptions,
	}
}
