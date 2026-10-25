package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/baseline"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/ownership"
	"github.com/vibe-shift-coding/kube-outlier/internal/report"
)

// parseWhyKey parses the --why identity argument, which is exactly
// identity.ResourceKey.String() syntax: "Kind name" (cluster-scoped),
// "Kind namespace/name", "group/Kind namespace/name".
func parseWhyKey(s string) (identity.ResourceKey, error) {
	key, err := identity.ParseKey(s)
	if err != nil {
		return identity.ResourceKey{}, fmt.Errorf("--why: %w", err)
	}
	return key, nil
}

// whyInputs is the scan-mode context buildWhy needs.
type whyInputs struct {
	results  []clusterResult
	cfg      *config.Config
	source   string               // "none" | "live" | "baseline"
	bref     *analyze.BaselineRef // baseline mode only
	active   map[identity.ResourceKey]baseline.ApprovedException
	reported []analyze.Finding // the findings the report shows, every class
	now      time.Time
}

// buildWhy assembles the full evidence for one identity. The likely-manual
// score and presence come from a minScore-0 re-run of the normal findings
// pipeline, so --why can never disagree with what a scan would have
// computed, including for resources below the minimum score. Every other
// finding the report shows for the identity (missing-from-cluster,
// content-differs) is named in the status, so --why and the report agree.
func buildWhy(key identity.ResourceKey, in whyInputs) (report.Explain, error) {
	e := report.Explain{Key: key, Source: in.source}

	for _, r := range in.results {
		for _, res := range r.snap.Resources {
			if res.Key != key {
				continue
			}
			var owners []string
			for _, or := range res.OwnerReferences {
				owners = append(owners, or.Kind+" "+or.Name)
			}
			e.Occurrences = append(e.Occurrences, report.ExplainOccurrence{
				Cluster:           r.snap.Cluster,
				Ownership:         ownership.Classify(res, in.cfg.Analysis),
				CreationTimestamp: res.CreationTimestamp,
				AgeDays:           int(in.now.Sub(res.CreationTimestamp.Time).Hours() / 24),
				OwnerReferences:   owners,
			})
		}
	}
	sort.Slice(e.Occurrences, func(i, j int) bool { return e.Occurrences[i].Cluster < e.Occurrences[j].Cluster })

	// The report's other findings about this identity.
	var other []string
	for _, f := range in.reported {
		if f.Key != key || f.Class == analyze.ClassLikelyManual {
			continue
		}
		other = append(other, reportedLine(f))
		if f.Class == analyze.ClassContentDiffers {
			e.Content = append(e.Content, f)
		}
		if e.Presence == nil {
			e.Presence = f.Presence
		}
	}

	if len(e.Occurrences) == 0 && len(other) == 0 {
		if in.bref != nil {
			if entry, ok := in.bref.Entries[key]; ok {
				e.Status = fmt.Sprintf("not reported: not on the scanned cluster(s); the baseline has it on %d/%d clusters, below analysis.commonThreshold (%.2f), so its absence is not reported",
					entry.ClusterCount, in.bref.FleetSize, in.cfg.Analysis.CommonThreshold)
				e.Presence = &analyze.Presence{PresentOn: []string{}, MissingFrom: []string{}, PresentCount: entry.ClusterCount, FleetSize: in.bref.FleetSize, Ratio: entry.Ratio}
				return e, nil
			}
		}
		return e, notFoundError(key, in)
	}

	own := ""
	if len(e.Occurrences) > 0 {
		own = ownStatus(key, &e, in)
	}

	exc, excepted := in.active[key]
	switch {
	case excepted:
		e.Status = fmt.Sprintf("suppressed by approved exception (approved by %s: %s)", exc.ApprovedBy, exc.Reason)
		if own != "" {
			e.Notes = []string{"without the exception: " + own}
		}
	case len(other) == 0:
		e.Status = own
	case strings.HasPrefix(own, "reported"):
		e.Status = own
		e.Notes = other
	default:
		e.Status = other[0]
		e.Notes = other[1:]
		if own != "" {
			e.Notes = append(e.Notes, "not reported as likely-manual: "+own)
		}
	}
	if len(e.Occurrences) == 0 {
		e.Notes = append(e.Notes, "not present on any scanned cluster")
	}
	return e, nil
}

// ownStatus is the likely-manual verdict for an identity found on at least
// one scanned cluster, filling e.Score and e.Presence when it was scored.
func ownStatus(key identity.ResourceKey, e *report.Explain, in whyInputs) string {
	// minScore-0 re-run: every candidate becomes a finding, so the score,
	// modifiers, and presence are exactly what the scan pipeline computes.
	cfgA := in.cfg.Analysis
	cfgA.MinScore = 0
	var findings []analyze.Finding
	switch {
	case in.bref != nil:
		for _, r := range in.results {
			served, failed := servedAndFailed(r)
			f, _ := analyze.BaselineFindings(r.snap, *in.bref, served, failed, cfgA, in.now)
			findings = append(findings, f...)
		}
	case len(in.results) >= 2:
		findings, _ = analyze.Tier2Findings(snapshotsOf(in.results), fleetInputs(in.results), cfgA, in.now)
	default:
		for _, r := range in.results {
			f, _ := analyze.Tier1Findings(r.snap, cfgA, in.now)
			findings = append(findings, f...)
		}
	}
	var found *analyze.Finding
	for i := range findings {
		if findings[i].Class == analyze.ClassLikelyManual && findings[i].Key == key {
			found = &findings[i]
			break
		}
	}

	systemObject := false
	for _, r := range in.results {
		for _, res := range r.snap.Resources {
			if res.Key == key && analyze.IsSystemObject(res) {
				systemObject = true
			}
		}
	}
	hasOwnerRefs := false
	for _, occ := range e.Occurrences {
		if len(occ.OwnerReferences) > 0 {
			hasOwnerRefs = true
		}
	}

	minScore := in.cfg.Analysis.MinScore
	switch {
	case systemObject:
		return "never reported: created by the Kubernetes control plane (system object)"
	case found == nil && hasOwnerRefs:
		return "skipped: controller-owned (ownerReferences present — the parent is the interesting object)"
	case found == nil:
		return "not scored: no likely-manual candidate on any scanned cluster"
	}
	e.Score = &found.Score
	if found.Presence != nil {
		e.Presence = found.Presence
	}
	if found.Score.Total >= minScore {
		return fmt.Sprintf("reported (score %d >= minimum score %d)", found.Score.Total, minScore)
	}
	return fmt.Sprintf("below the minimum score (score %d < %d)", found.Score.Total, minScore)
}

// reportedLine is the --why status line for a reported finding that is not
// about one object's ownership.
func reportedLine(f analyze.Finding) string {
	switch f.Class {
	case analyze.ClassMissingFromCluster:
		line := "reported as missing-from-cluster: missing from " + strings.Join(f.Clusters, ", ")
		if p := f.Presence; p != nil {
			line += fmt.Sprintf(" (present on %d/%d clusters)", p.PresentCount, p.FleetSize)
		}
		return line
	case analyze.ClassContentDiffers:
		reference := "the majority version"
		if f.Content != nil && f.Content.Ambiguous {
			reference = "the most common version (no majority)"
		}
		return fmt.Sprintf("reported as content-differs: the copy on %s differs from %s", strings.Join(f.Clusters, ", "), reference)
	default:
		return "reported as " + string(f.Class)
	}
}

// notFoundError says why --why found nothing, when the scan can tell: a kind
// spelled without its group or in the wrong case, a cluster-scoped kind
// without --include-cluster-scoped, an excluded kind or namespace. Otherwise
// the resource doesn't exist on the scanned clusters (a typo in the name, or
// it was deleted).
func notFoundError(key identity.ResourceKey, in whyInputs) error {
	msg := key.String() + " not found in scope on any scanned cluster"
	if hint := notFoundHint(key, in); hint != "" {
		msg += "; " + hint
	}
	return fmt.Errorf("%s", msg)
}

func notFoundHint(key identity.ResourceKey, in whyInputs) string {
	scope := in.cfg.Scope
	ref := config.KindRef{Group: key.Group, Kind: key.Kind}
	kind := kindString(ref)
	served := servedKinds(in.results)
	namespaced, isServed := served[ref]

	switch {
	case len(served) > 0 && !isServed:
		if s := kindSuggestions(ref, served); len(s) > 0 {
			guesses := make([]string, len(s))
			for i, k := range s {
				guesses[i] = k + " " + strings.TrimPrefix(key.String(), kind+" ")
			}
			return fmt.Sprintf("did you mean %s?", quoteJoin(guesses))
		}
		return fmt.Sprintf("no scanned cluster serves the kind %s", kind)
	case isServed && !namespaced && key.Namespace != "":
		return fmt.Sprintf("%s is cluster-scoped: write it without a namespace, as \"%s %s\"", kind, kind, key.Name)
	case isServed && namespaced && key.Namespace == "":
		return fmt.Sprintf("%s is namespaced: write it as \"%s <namespace>/%s\"", kind, kind, key.Name)
	case key.Namespace == "" && !scope.IncludeClusterScoped:
		return fmt.Sprintf("%s is cluster-scoped; add --include-cluster-scoped", kind)
	}

	for _, e := range scope.Kinds.Exclude {
		if e == kind {
			if config.IsBuiltinKindExclude(e) {
				return fmt.Sprintf("%s is never scanned (a built-in exclusion)", kind)
			}
			return fmt.Sprintf("%s is excluded by scope.kinds.exclude", kind)
		}
	}
	if len(scope.Kinds.Include) > 0 && !contains(scope.Kinds.Include, kind) {
		return fmt.Sprintf("%s is not in scope.kinds.include", kind)
	}

	ns := key.Namespace
	if ns == "" {
		return ""
	}
	if len(scope.Namespaces.Include) > 0 {
		if !contains(scope.Namespaces.Include, ns) {
			return fmt.Sprintf("namespace %s is not among the -n namespaces", ns)
		}
		return ""
	}
	if contains(scope.Namespaces.Exclude, ns) {
		if contains(config.Default().Scope.Namespaces.Exclude, ns) {
			return fmt.Sprintf("namespace %s is excluded by default; scan it with -n %s", ns, ns)
		}
		return fmt.Sprintf("namespace %s is excluded by scope.namespaces.exclude; scan it with -n %s", ns, ns)
	}
	for _, p := range scope.Namespaces.ExcludePatterns {
		if re, err := regexp.Compile("^(?:" + p + ")$"); err == nil && re.MatchString(ns) {
			return fmt.Sprintf("namespace %s matches scope.namespaces.excludePatterns %q; scan it with -n %s", ns, p, ns)
		}
	}
	return ""
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
