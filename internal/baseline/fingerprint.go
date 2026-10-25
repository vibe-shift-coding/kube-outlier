package baseline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
)

// FingerprintScope canonicalizes a scan scope into its fingerprint: lists are
// copied and sorted (the loaded config already has built-in defaults merged
// into the exclude lists), and Hash is the sha256 hex of the canonical JSON
// (sorted keys, no whitespace) of the other fields.
func FingerprintScope(scope config.ScopeConfig) ScopeFingerprint {
	fp := ScopeFingerprint{
		IncludeClusterScoped:     scope.IncludeClusterScoped,
		NamespaceInclude:         dedupSorted(scope.Namespaces.Include),
		NamespaceExclude:         sortedCopy(scope.Namespaces.Exclude),
		NamespaceExcludePatterns: sortedCopy(scope.Namespaces.ExcludePatterns),
		KindExclude:              sortedCopy(scope.Kinds.Exclude),
		KindInclude:              sortedCopy(scope.Kinds.Include),
	}
	fp.Hash = fp.computeHash()
	return fp
}

// computeHash hashes every field except Hash itself. encoding/json emits
// struct fields in declaration order with no whitespace, which is canonical
// enough for a struct of scalars and pre-sorted string slices.
func (fp ScopeFingerprint) computeHash() string {
	hashless := fp
	hashless.Hash = ""
	raw, err := json.Marshal(hashless)
	if err != nil {
		// A struct of bools/strings/[]string cannot fail to marshal.
		panic("baseline: marshalling scope fingerprint: " + err.Error())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Diff renders a field-by-field comparison of two fingerprints (a = baseline,
// b = current scan) for the fatal scope-mismatch message: one line per
// differing field, list differences as +added/-removed relative to a.
func Diff(a, b ScopeFingerprint) []string {
	var lines []string
	if a.IncludeClusterScoped != b.IncludeClusterScoped {
		lines = append(lines, fmt.Sprintf("includeClusterScoped: baseline=%v scan=%v",
			a.IncludeClusterScoped, b.IncludeClusterScoped))
	}
	lines = append(lines, diffList("namespaceInclude", a.NamespaceInclude, b.NamespaceInclude)...)
	lines = append(lines, diffList("namespaceExclude", a.NamespaceExclude, b.NamespaceExclude)...)
	lines = append(lines, diffList("namespaceExcludePatterns", a.NamespaceExcludePatterns, b.NamespaceExcludePatterns)...)
	lines = append(lines, diffList("kindExclude", a.KindExclude, b.KindExclude)...)
	lines = append(lines, diffList("kindInclude", a.KindInclude, b.KindInclude)...)
	return lines
}

func diffList(field string, a, b []string) []string {
	inA := toSet(a)
	inB := toSet(b)
	var parts []string
	for _, v := range b {
		if !inA[v] {
			parts = append(parts, "+"+v)
		}
	}
	for _, v := range a {
		if !inB[v] {
			parts = append(parts, "-"+v)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	sort.Strings(parts)
	return []string{fmt.Sprintf("%s: %v", field, parts)}
}

func toSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

// dedupSorted returns the distinct values of s, sorted; nil when s is empty
// so an unrestricted scope's fingerprint (and hash) is unchanged.
func dedupSorted(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	set := toSet(s)
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func sortedCopy(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	sort.Strings(out)
	return out
}
