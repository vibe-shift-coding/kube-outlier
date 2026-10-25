// Package report builds the JSON/table report envelope and computes the
// process exit code (JSON schema and exit codes frozen in
// docs/interfaces.md §3, §3b).
package report

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/connect"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

const schemaVersion = 1

// Report is the top-level JSON report envelope (interfaces.md §3).
type Report struct {
	SchemaVersion int           `json:"schemaVersion"`
	ToolVersion   string        `json:"toolVersion"`
	GeneratedAt   time.Time     `json:"generatedAt"`
	Fleet         FleetSummary  `json:"fleet"`
	Scope         ScopeSummary  `json:"scope"`
	FleetPresence FleetPresence `json:"fleetPresence"`
	SkippedGVRs   []SkippedGVR  `json:"skippedGVRs"`
	// SkippedObjects are the copies the content comparison could not read
	// (a forbidden get, a timeout). Set by the caller after Build, like
	// ExitCode; omitted when empty, so it never appears without
	// --compare-content.
	SkippedObjects []SkippedObject   `json:"skippedObjects,omitempty"`
	Findings       []analyze.Finding `json:"findings"`
	Summary        Summary           `json:"summary"`
	// Partial is true when anything was skipped: a requested cluster, a
	// resource type or API group, or (with the content comparison) a copy it
	// could not read. It does not depend on allowPartial, which only changes
	// the exit code. Set by the caller after Build, with SkippedObjects.
	Partial  bool `json:"partial"`
	ExitCode int  `json:"exitCode"`

	// Listings is how many listings each cluster's scan attempted, keyed by
	// cluster name. Not part of the JSON: it lets SkipSummaryLines say "every
	// type was forbidden" in one line. Set by the caller; nil is fine.
	Listings map[string]ListingCounts `json:"-"`
}

// ListingCounts is how many listings one cluster's scan attempted: with -n,
// namespaced types once per named namespace (PerNamespace) and cluster-scoped
// types once (ClusterWide); without -n, every type once (ClusterWide).
type ListingCounts struct {
	ClusterWide  int
	PerNamespace map[string]int
}

// FleetSummary is the requested fleet and which clusters answered.
type FleetSummary struct {
	Requested int           `json:"requested"`
	Reachable int           `json:"reachable"`
	Clusters  []ClusterInfo `json:"clusters"`
}

// ClusterInfo is one requested cluster's connection result.
type ClusterInfo struct {
	Name          string `json:"name"`
	Context       string `json:"context"`
	Reachable     bool   `json:"reachable"`
	ServerVersion string `json:"serverVersion,omitempty"`
	SkipReason    string `json:"skipReason,omitempty"`
}

// ScopeSummary echoes the effective scan scope, built-in exclusions merged in.
type ScopeSummary struct {
	IncludeClusterScoped     bool     `json:"includeClusterScoped"`
	NamespaceInclude         []string `json:"namespaceInclude,omitempty"`
	NamespaceExclude         []string `json:"namespaceExclude"`
	NamespaceExcludePatterns []string `json:"namespaceExcludePatterns"`
	KindExclude              []string `json:"kindExclude"`
	KindInclude              []string `json:"kindInclude"`
}

// FleetPresence says whether the fleet presence signal applied and where its
// data came from. Source is one of "none" | "live" | "baseline".
// Single-cluster scans are always {applied: false, source: "none"}; scans with
// two or more snapshotted clusters are {applied: true, source: "live"}; scans
// against a baseline artifact are {applied: true, source: "baseline"}.
type FleetPresence struct {
	Applied bool   `json:"applied"`
	Source  string `json:"source"`
}

// SkippedGVR is one GVR that could not be listed on one cluster.
type SkippedGVR struct {
	Cluster   string `json:"cluster"`
	GVR       string `json:"gvr"`
	Namespace string `json:"namespace,omitempty"` // set when the listing was per-namespace
	Reason    string `json:"reason"`
	Forbidden bool   `json:"forbidden"`
}

// SkippedObject is one object on one cluster that the content comparison
// could not read, so its copy was left out of the comparison.
type SkippedObject struct {
	Cluster   string               `json:"cluster"`
	Resource  identity.ResourceKey `json:"resource"`
	Reason    string               `json:"reason"`
	Forbidden bool                 `json:"forbidden"`
}

// Summary counts the findings reported and those held back.
type Summary struct {
	Total                   int            `json:"total"`
	ByClass                 map[string]int `json:"byClass"`
	SuppressedBelowMinScore int            `json:"suppressedBelowMinScore"`
	// SuppressedByException counts findings dropped by a baseline's
	// approved-exceptions block (ADR-0004). omitempty keeps it out
	// of non-baseline output. Set by the caller after
	// Build (like ExitCode): only cmd knows about exceptions.
	SuppressedByException int `json:"suppressedByException,omitempty"`
}

// Build assembles the report envelope. findings and suppressed come from
// analyze.Tier1Findings (single cluster) or analyze.Tier2Findings (N >= 2,
// in which case fleetPresence is {applied: true, source: "live"}); Build
// re-sorts findings into the single global order the JSON/table output
// requires. ExitCode is left at its zero value — the caller computes it via
// ExitCode() and assigns it before rendering (interfaces.md §3 note).
func Build(refs []connect.ClusterRef, scope config.ScopeConfig, snaps []*inventory.Snapshot, findings []analyze.Finding, suppressed int, presence FleetPresence, toolVersion string, now time.Time) *Report {
	sorted := make([]analyze.Finding, len(findings))
	copy(sorted, findings)
	analyze.SortFindings(sorted)

	clusters := make([]ClusterInfo, 0, len(refs))
	reachable := 0
	for _, r := range refs {
		if r.Reachable {
			reachable++
		}
		clusters = append(clusters, ClusterInfo{
			Name:          r.Name,
			Context:       r.Context,
			Reachable:     r.Reachable,
			ServerVersion: r.ServerVersion,
			SkipReason:    r.SkipReason,
		})
	}

	skippedGVRs := []SkippedGVR{}
	for _, snap := range snaps {
		for _, e := range snap.Errors {
			reason := e.Err
			if e.Forbidden {
				reason = "forbidden"
			}
			skippedGVRs = append(skippedGVRs, SkippedGVR{
				Cluster:   snap.Cluster,
				GVR:       gvrString(e.GVR),
				Namespace: e.Namespace,
				Reason:    reason,
				Forbidden: e.Forbidden,
			})
		}
	}

	byClass := make(map[string]int, len(sorted))
	for _, f := range sorted {
		byClass[string(f.Class)]++
	}

	return &Report{
		SchemaVersion: schemaVersion,
		ToolVersion:   toolVersion,
		GeneratedAt:   now.UTC(),
		Fleet: FleetSummary{
			Requested: len(refs),
			Reachable: reachable,
			Clusters:  clusters,
		},
		Scope: ScopeSummary{
			IncludeClusterScoped:     scope.IncludeClusterScoped,
			NamespaceInclude:         scope.Namespaces.Include,
			NamespaceExclude:         emptyIfNil(scope.Namespaces.Exclude),
			NamespaceExcludePatterns: emptyIfNil(scope.Namespaces.ExcludePatterns),
			KindExclude:              emptyIfNil(scope.Kinds.Exclude),
			KindInclude:              emptyIfNil(scope.Kinds.Include),
		},
		FleetPresence: presence,
		SkippedGVRs:   skippedGVRs,
		Findings:      sorted,
		Summary: Summary{
			Total:                   len(sorted),
			ByClass:                 byClass,
			SuppressedBelowMinScore: suppressed,
		},
	}
}

// IsPartial reports whether anything was skipped: a requested cluster, a
// resource type or API group, or a copy the content comparison could not read.
func IsPartial(rep *Report) bool {
	if len(rep.SkippedGVRs) > 0 || len(rep.SkippedObjects) > 0 {
		return true
	}
	for _, c := range rep.Fleet.Clusters {
		if !c.Reachable {
			return true
		}
	}
	return false
}

// SummaryLine is the one-line count of what the report shows and holds back,
// for the end of a markdown report and (on stderr) a table.
func SummaryLine(rep *Report) string {
	line := fmt.Sprintf("%d finding(s), %d below the minimum score", rep.Summary.Total, rep.Summary.SuppressedBelowMinScore)
	if rep.Summary.SuppressedByException > 0 {
		line += fmt.Sprintf(", %d suppressed by exception", rep.Summary.SuppressedByException)
	}
	return line + "."
}

// NothingRead reports whether no cluster could list anything at all: every
// listing each reachable cluster attempted failed. "No findings" would then
// say nothing about the clusters. False when rep.Listings is not set.
func NothingRead(rep *Report) bool {
	if len(rep.Listings) == 0 {
		return false
	}
	failed := map[string]int{}
	for _, g := range rep.SkippedGVRs {
		if !strings.HasSuffix(g.GVR, "/*") { // a failed API group is not a listing
			failed[g.Cluster]++
		}
	}
	for cluster, lc := range rep.Listings {
		attempted := lc.ClusterWide
		for _, n := range lc.PerNamespace {
			attempted += n
		}
		if attempted == 0 || failed[cluster] < attempted {
			return false
		}
	}
	return true
}

// SkipSummaryLines renders one human-readable line per skipped cluster, per
// skipped GVR and per unread object, for the CLI to print to stderr regardless of output
// format (JSON or table both need this — it's diagnostic, not report data).
// When every listing of a cluster (or of one -n namespace on it) was
// forbidden, one line says so instead of one line per type, with the likely
// fix: an identity that can read only some namespaces, or a namespace that
// doesn't exist.
func SkipSummaryLines(rep *Report) []string {
	var lines []string
	for _, c := range rep.Fleet.Clusters {
		if !c.Reachable {
			lines = append(lines, fmt.Sprintf("skipped cluster %s: %s", c.Name, c.SkipReason))
		}
	}

	type group struct{ cluster, namespace string }
	forbidden := map[group]int{}
	for _, g := range rep.SkippedGVRs {
		if g.Forbidden {
			forbidden[group{g.Cluster, g.Namespace}]++
		}
	}
	collapsed := func(gr group) (string, bool) {
		lc, ok := rep.Listings[gr.cluster]
		if !ok {
			return "", false
		}
		n := forbidden[gr]
		switch {
		case gr.namespace != "":
			if n < 2 || n != lc.PerNamespace[gr.namespace] {
				return "", false
			}
			return fmt.Sprintf("%s: all %d resource types in namespace %s forbidden, skipped; does the namespace exist, and can this identity read it?", gr.cluster, n, gr.namespace), true
		case n < 2 || n != lc.ClusterWide:
			return "", false
		case len(lc.PerNamespace) > 0:
			return fmt.Sprintf("%s: all %d cluster-scoped resource types forbidden, skipped; leave out --include-cluster-scoped if this identity can't read them", gr.cluster, n), true
		default:
			return fmt.Sprintf("%s: all %d resource types forbidden cluster-wide, skipped; if you can read only some namespaces, scan them with -n <namespace>", gr.cluster, n), true
		}
	}

	done := map[group]bool{}
	for _, g := range rep.SkippedGVRs {
		gr := group{g.Cluster, g.Namespace}
		if g.Forbidden {
			if done[gr] {
				continue
			}
			if line, ok := collapsed(gr); ok {
				done[gr] = true
				lines = append(lines, line)
				continue
			}
		}
		where := g.GVR
		if g.Namespace != "" {
			where += " in namespace " + g.Namespace
		}
		if g.Forbidden {
			lines = append(lines, fmt.Sprintf("%s: %s forbidden, skipped", g.Cluster, where))
		} else {
			lines = append(lines, fmt.Sprintf("%s: %s skipped: %s", g.Cluster, where, g.Reason))
		}
	}
	for _, o := range rep.SkippedObjects {
		if o.Forbidden {
			lines = append(lines, fmt.Sprintf("%s: get %s forbidden, left out of the content comparison", o.Cluster, o.Resource.String()))
		} else {
			lines = append(lines, fmt.Sprintf("%s: get %s failed, left out of the content comparison: %s", o.Cluster, o.Resource.String(), o.Reason))
		}
	}
	return lines
}

// emptyIfNil returns a non-nil empty slice for a nil input so the field
// marshals to a JSON array ("[]"), not "null" — the frozen §3 schema documents
// these scope/skippedGVRs fields as arrays, and CI consumers rely on that.
func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// gvrString renders a GVR as "group/version/resource" (core group omits the
// leading "/"), e.g. "v1/secrets" or "networking.k8s.io/v1/networkpolicies".
func gvrString(gvr schema.GroupVersionResource) string {
	return gvr.GroupVersion().String() + "/" + gvr.Resource
}
