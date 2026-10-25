package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/vibe-shift-coding/kube-outlier/internal/baseline"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/report"
)

// kindString renders a KindRef in the config's wire syntax: "group/Kind", or
// a bare "Kind" for the core group.
func kindString(k config.KindRef) string {
	if k.Group == "" {
		return k.Kind
	}
	return k.Group + "/" + k.Kind
}

// servedKinds is every kind any scanned cluster serves, before the scope
// filters, mapped to whether it is namespaced. Empty when the results carry
// no discovery data (as in tests), so callers skip their checks.
func servedKinds(results []clusterResult) map[config.KindRef]bool {
	out := map[config.KindRef]bool{}
	for _, r := range results {
		for _, gi := range r.served {
			out[config.KindRef{Group: gi.GVR.Group, Kind: gi.Kind}] = gi.Namespaced
		}
	}
	return out
}

// scannedKinds is every kind a scan listed on at least one cluster: the
// served kinds that the scope kept.
func scannedKinds(results []clusterResult) map[config.KindRef]bool {
	out := map[config.KindRef]bool{}
	for _, r := range results {
		for _, gi := range r.gvrs {
			out[config.KindRef{Group: gi.GVR.Group, Kind: gi.Kind}] = true
		}
	}
	return out
}

// kindSuggestions returns the served kinds a mistyped ref probably meant: the
// same kind name in any group or letter case ("NetworkPolicy" for
// "networking.k8s.io/NetworkPolicy", "configmap" for "ConfigMap"), sorted.
func kindSuggestions(ref config.KindRef, served map[config.KindRef]bool) []string {
	var out []string
	for k := range served {
		if k != ref && strings.EqualFold(k.Kind, ref.Kind) {
			out = append(out, kindString(k))
		}
	}
	sort.Strings(out)
	return out
}

// quoteJoin renders `"a"`, or `"a" or "b"` for several choices.
func quoteJoin(choices []string) string {
	quoted := make([]string, len(choices))
	for i, c := range choices {
		quoted[i] = fmt.Sprintf("%q", c)
	}
	return strings.Join(quoted, " or ")
}

// unmatchedKind is the warning for a kind in setting that no scanned cluster
// serves: usually a missing group ("Deployment" for "apps/Deployment").
func unmatchedKind(setting, entry string, served map[config.KindRef]bool) string {
	ref, err := config.ParseKindRef(entry)
	msg := fmt.Sprintf("warning: %s: %q matches no resource type on any scanned cluster", setting, entry)
	if err == nil {
		if s := kindSuggestions(ref, served); len(s) > 0 {
			msg += fmt.Sprintf(" (did you mean %s?)", quoteJoin(s))
		} else if ref.Group == "" {
			msg += " (kinds outside the core group need their group, e.g. apps/Deployment)"
		}
	}
	return msg
}

// warnUnmatchedKinds warns about scope.kinds entries that match no type any
// scanned cluster serves, so a misspelled exclusion or allowlist entry can't
// silently do nothing. The built-in exclusions are never reported.
func warnUnmatchedKinds(w io.Writer, results []clusterResult, cfg *config.Config) {
	served := servedKinds(results)
	if len(served) == 0 {
		return
	}
	check := func(setting string, entries []string) {
		for _, e := range entries {
			if config.IsBuiltinKindExclude(e) {
				continue
			}
			if ref, err := config.ParseKindRef(e); err == nil {
				if _, ok := served[ref]; ok {
					continue
				}
			}
			_, _ = fmt.Fprintln(w, unmatchedKind(setting, e, served))
		}
	}
	check("scope.kinds.exclude", cfg.Scope.Kinds.Exclude)
	check("scope.kinds.include", cfg.Scope.Kinds.Include)
}

// contentKindsInScope warns about each content.kinds and sensitive.kinds entry
// that matches nothing the scan listed, and returns how many content.kinds
// entries do match. Without discovery data (tests) every entry counts as
// matching.
func contentKindsInScope(w io.Writer, results []clusterResult, cfg *config.Config) int {
	served := servedKinds(results)
	if len(served) == 0 {
		return len(cfg.Content.Kinds)
	}
	scanned := scannedKinds(results)
	matched := 0
	for _, e := range cfg.Content.Kinds {
		ref, err := config.ParseKindRef(e)
		if err != nil {
			continue // Validate already rejected it
		}
		_, isServed := served[ref]
		switch {
		case scanned[ref]:
			matched++
		case isServed:
			_, _ = fmt.Fprintf(w, "warning: content.kinds: %q is outside the scan scope (scope.kinds, --include-cluster-scoped), so it is not compared\n", e)
		default:
			_, _ = fmt.Fprintln(w, unmatchedKind("content.kinds", e, served))
		}
	}
	// A misspelled sensitive kind would leave a credential-holding kind's
	// values in plain text in the comparison.
	for _, e := range cfg.Sensitive.Kinds {
		if ref, err := config.ParseKindRef(e); err == nil {
			if _, ok := served[ref]; !ok {
				_, _ = fmt.Fprintln(w, unmatchedKind("sensitive.kinds", e, served))
			}
		}
	}
	return matched
}

// warnUnknownExceptions warns about each active baseline exception that names
// a resource neither the baseline nor any scanned cluster has: almost always
// a kind written without its group, which would silently suppress nothing.
// An exception for a resource on another cluster of the baseline is fine.
func warnUnknownExceptions(w io.Writer, base *loadedBaseline, active map[identity.ResourceKey]baseline.ApprovedException, results []clusterResult) {
	known := map[identity.ResourceKey]bool{}
	for _, e := range base.b.Entries {
		known[e.Key.ResourceKey()] = true
	}
	for _, r := range results {
		for _, res := range r.snap.Resources {
			known[res.Key] = true
		}
	}
	keys := make([]identity.ResourceKey, 0, len(active))
	for k := range active {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	for _, k := range keys {
		if known[k] {
			continue
		}
		var guesses []string
		for kk := range known {
			if kk.Namespace == k.Namespace && kk.Name == k.Name && strings.EqualFold(kk.Kind, k.Kind) {
				guesses = append(guesses, kk.String())
			}
		}
		sort.Strings(guesses)
		msg := fmt.Sprintf("warning: baseline exception %q matches no resource in the baseline or on the scanned cluster(s)", k.String())
		if len(guesses) > 0 {
			msg += fmt.Sprintf(" (did you mean %s?)", quoteJoin(guesses))
		}
		_, _ = fmt.Fprintln(w, msg)
	}
}

// listingCounts is how many listings each cluster's scan attempted (see
// report.ListingCounts), so the skip summary can collapse "everything was
// forbidden" into one line.
func listingCounts(results []clusterResult, cfg *config.Config) map[string]report.ListingCounts {
	namespaces := map[string]bool{}
	for _, ns := range cfg.Scope.Namespaces.Include {
		namespaces[ns] = true
	}
	out := make(map[string]report.ListingCounts, len(results))
	for _, r := range results {
		namespaced := 0
		for _, gi := range r.gvrs {
			if gi.Namespaced {
				namespaced++
			}
		}
		lc := report.ListingCounts{ClusterWide: len(r.gvrs)}
		if len(namespaces) > 0 {
			lc.ClusterWide = len(r.gvrs) - namespaced
			lc.PerNamespace = make(map[string]int, len(namespaces))
			for ns := range namespaces {
				lc.PerNamespace[ns] = namespaced
			}
		}
		out[r.snap.Cluster] = lc
	}
	return out
}

// terminalWidth is the width of the terminal w writes to, or 0 when w is not
// a terminal (a pipe or file), so redirected output never changes.
func terminalWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return 0
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return width
}
