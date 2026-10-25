// Package normalize strips server bookkeeping from resource bodies (shapes
// frozen in docs/interfaces.md §2.8) and canonicalizes them for the content
// comparison.
// Strip rules are data, not code: see the embedded defaults.yaml.
package normalize

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
)

//go:embed defaults.yaml
var defaultsYAML []byte

// ruleSet is one block of strip rules (global or per-kind).
type ruleSet struct {
	Drop                   []string `json:"drop"`
	DropAnnotations        []string `json:"dropAnnotations"`
	DropAnnotationPrefixes []string `json:"dropAnnotationPrefixes"`
}

// comparisonRules are the comparison-only aggressive strips and quantity paths.
type comparisonRules struct {
	Drop          []string `json:"drop"`
	QuantityPaths []string `json:"quantityPaths"`
}

type defaultsFile struct {
	Global            ruleSet                    `json:"global"`
	PerKind           map[string]ruleSet         `json:"perKind"`
	ComparisonPerKind map[string]comparisonRules `json:"comparisonPerKind"`
}

var defaults defaultsFile

func init() {
	if err := yaml.Unmarshal(defaultsYAML, &defaults); err != nil {
		panic("normalize: invalid embedded defaults.yaml: " + err.Error())
	}
}

func kindKeyOf(key identity.ResourceKey) string {
	if key.Group != "" {
		return key.Group + "/" + key.Kind
	}
	return key.Kind
}

// Normalize returns a deep-copied body with server bookkeeping stripped.
// Pure: the input is never mutated. Paths that do not exist are no-ops, so
// the rules are safe across API versions.
func Normalize(body map[string]interface{}, key identity.ResourceKey) map[string]interface{} {
	out := runtime.DeepCopyJSON(body)

	sets := []ruleSet{defaults.Global}
	if perKind, ok := defaults.PerKind[kindKeyOf(key)]; ok {
		sets = append(sets, perKind)
	}
	for _, rs := range sets {
		for _, path := range rs.Drop {
			dropParsed(out, parsePath(path))
		}
		dropAnnotations(out, rs.DropAnnotations, rs.DropAnnotationPrefixes)
	}

	dropEmptyMetadataMap(out, "annotations")
	dropEmptyMetadataMap(out, "labels")
	return out
}

// CanonicalizeForComparison returns the content-comparison form of a body (ADR-0006):
// Normalize, then per-kind aggressive server-default strips, user extraIgnore
// paths, Secret-data hashing, quantity canonicalization, and empty-map/slice
// removal. sensitive marks a sensitive.kinds kind other than Secret: every
// value outside apiVersion, kind and metadata is hashed too (ADR-0009). Pure —
// the input is never mutated. Normalize alone stays conservative; only this
// over-strips.
func CanonicalizeForComparison(body map[string]interface{}, key identity.ResourceKey, extraIgnore []string, sensitive bool) map[string]interface{} {
	out := Normalize(body, key) // already a fresh deep copy

	if sensitive && (key.Group != "" || key.Kind != "Secret") {
		hashAllValuesInPlace(out)
	} else {
		hashSecretInPlace(out)
	}

	if dr, ok := defaults.ComparisonPerKind[kindKeyOf(key)]; ok {
		for _, path := range dr.Drop {
			dropParsed(out, parsePath(path))
		}
		for _, path := range dr.QuantityPaths {
			canonicalizeQuantities(out, parsePath(path))
		}
	}
	for _, path := range extraIgnore {
		dropParsed(out, parsePath(path))
	}

	removeEmpties(out)
	return out
}

// CanonicalJSON marshals a body to canonical JSON: encoding/json sorts map
// keys and emits no insignificant whitespace; array order is preserved.
func CanonicalJSON(body map[string]interface{}) ([]byte, error) {
	return json.Marshal(body)
}

// CanonicalHash is the sha256 hex of CanonicalJSON — the content hash used
// for majority grouping in the content comparison.
func CanonicalHash(body map[string]interface{}) (string, error) {
	raw, err := CanonicalJSON(body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// HashSecretData replaces every Secret data/stringData value with
// "sha256:<hex>", the comparison form. Returns the
// deep-copied body and whether anything was hashed.
func HashSecretData(body map[string]interface{}) (map[string]interface{}, bool) {
	out := runtime.DeepCopyJSON(body)
	return out, hashSecretInPlace(out)
}

// isSecretBody reports whether body is a Secret, the kind whose data and
// stringData values are always hashed.
func isSecretBody(body map[string]interface{}) bool {
	kind, _ := body["kind"].(string)
	return kind == "Secret"
}

// hashSecretInPlace hashes a Secret's data/stringData values in place.
func hashSecretInPlace(body map[string]interface{}) bool {
	if !isSecretBody(body) {
		return false
	}
	hashed := false
	for _, field := range []string{"data", "stringData"} {
		values, ok := body[field].(map[string]interface{})
		if !ok {
			continue
		}
		for k, v := range values {
			s, _ := v.(string)
			sum := sha256.Sum256([]byte(s))
			values[k] = "sha256:" + hex.EncodeToString(sum[:])
			hashed = true
		}
	}
	return hashed
}

// canonicalizeQuantities rewrites each value reached by steps to its canonical
// resource.Quantity string (100m == 0.1, 1Gi == 1024Mi). Values that don't
// parse as a quantity are left untouched.
func canonicalizeQuantities(root map[string]interface{}, steps []pathStep) {
	for _, lf := range resolveLeaves(root, steps) {
		s, ok := lf.get().(string)
		if !ok {
			continue
		}
		q, err := resource.ParseQuantity(s)
		if err != nil {
			continue
		}
		lf.set(q.String())
	}
}

// removeEmpties deletes empty map / empty slice VALUES from every map in the
// tree (bottom-up), so {} and [] compare equal to an absent field (ADR-0006:
// empty ⇔ nil). Array elements are recursed into but never removed (positional
// meaning). Comparison-only — Normalize alone keeps empty containers.
func removeEmpties(v interface{}) {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, val := range t {
			removeEmpties(val)
			if isEmptyContainer(val) {
				delete(t, k)
			}
		}
	case []interface{}:
		for _, el := range t {
			removeEmpties(el)
		}
	}
}

func isEmptyContainer(v interface{}) bool {
	switch t := v.(type) {
	case map[string]interface{}:
		return len(t) == 0
	case []interface{}:
		return len(t) == 0
	default:
		return false
	}
}

// hashAllValuesInPlace replaces every value outside apiVersion, kind and
// metadata with "sha256:<hex>" of its JSON encoding: the comparison form for
// sensitive.kinds kinds. Equal values still hash equal, so copies compare.
func hashAllValuesInPlace(body map[string]interface{}) {
	for _, field := range valueFields(body) {
		body[field] = mapLeaves(body[field], func(v interface{}) interface{} {
			raw, _ := json.Marshal(v)
			sum := sha256.Sum256(raw)
			return "sha256:" + hex.EncodeToString(sum[:])
		})
	}
}

// valueFields returns the top-level fields of body that can hold values:
// everything except apiVersion, kind and metadata.
func valueFields(body map[string]interface{}) []string {
	var fields []string
	for k := range body {
		switch k {
		case "apiVersion", "kind", "metadata":
		default:
			fields = append(fields, k)
		}
	}
	return fields
}

// mapLeaves returns v with every scalar (string, number, bool, null) replaced
// by f(scalar); maps and lists are rebuilt with the same keys and lengths.
func mapLeaves(v interface{}, f func(interface{}) interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, e := range t {
			out[k] = mapLeaves(e, f)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, e := range t {
			out[i] = mapLeaves(e, f)
		}
		return out
	default:
		return f(v)
	}
}

// pathStep is one step of a parsed field path. A step names a map key; array
// marks that the key's value is an array to iterate; index (when indexed)
// picks one element of that array; mapWild marks a terminal "*" meaning
// "every value of the map reached so far".
type pathStep struct {
	key     string
	array   bool // "[*]": after descending into key, iterate the array's elements
	indexed bool // "[N]": after descending into key, take element index only
	index   int
	mapWild bool // ".*": terminal; the current node is a map, act on every value
}

// parsePath tokenizes a field path. Grammar: dot-separated keys, "[*]" for an
// array wildcard on the preceding key, "[N]" for one element of it,
// ["quoted.key"] for a key containing dots, and a trailing ".*" for every
// value of a map. A path starting with "/" is a JSON pointer (RFC 6901), the
// form the JSON report uses. Examples:
//
//	spec.ports[*].nodePort
//	spec.template.spec.containers[0].image
//	metadata.annotations["deployment.kubernetes.io/revision"]
//	spec.template.spec.containers[*].resources.limits.*
//	/spec/template/spec/containers/0/image
func parsePath(s string) []pathStep {
	if strings.HasPrefix(s, "/") {
		return parsePointer(s)
	}
	var steps []pathStep
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			steps = append(steps, pathStep{key: cur.String()})
			cur.Reset()
		}
	}
	for i := 0; i < len(s); {
		switch c := s[i]; c {
		case '.':
			flush()
			i++
			if i < len(s) && s[i] == '*' && i+1 == len(s) {
				steps = append(steps, pathStep{mapWild: true})
				i++
			}
		case '[':
			j := strings.IndexByte(s[i:], ']')
			if j < 0 { // malformed: treat the remainder as a literal key
				cur.WriteString(s[i:])
				i = len(s)
				continue
			}
			inner := s[i+1 : i+j]
			flush()
			n, isIndex := arrayIndex(inner)
			switch {
			case inner == "*":
				if len(steps) > 0 {
					steps[len(steps)-1].array = true
				}
			case isIndex:
				if len(steps) > 0 {
					steps[len(steps)-1].indexed, steps[len(steps)-1].index = true, n
				}
			default:
				steps = append(steps, pathStep{key: strings.Trim(inner, `"`)})
			}
			i += j + 1
		default:
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return steps
}

// parsePointer parses a JSON pointer ("/spec/containers/0/image"): each
// reference token is a key, except that a number after a key picks an element
// of that key's array. "~1" and "~0" unescape to "/" and "~".
func parsePointer(s string) []pathStep {
	var steps []pathStep
	for _, tok := range strings.Split(strings.TrimPrefix(s, "/"), "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		if n, ok := arrayIndex(tok); ok && len(steps) > 0 && !steps[len(steps)-1].indexed {
			steps[len(steps)-1].indexed, steps[len(steps)-1].index = true, n
			continue
		}
		steps = append(steps, pathStep{key: tok})
	}
	return steps
}

// arrayIndex reports whether s is a non-negative decimal array index.
func arrayIndex(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// leaf is the site a path addresses, where a drop or value rewrite happens:
// parent[key], or with inList the element index of the array parent[key].
type leaf struct {
	parent map[string]interface{}
	key    string
	inList bool
	index  int
}

// exists reports whether the addressed value is present.
func (lf leaf) exists() bool {
	v, ok := lf.parent[lf.key]
	if !ok || !lf.inList {
		return ok
	}
	arr, ok := v.([]interface{})
	return ok && lf.index < len(arr)
}

// get returns the addressed value, or nil when it is absent.
func (lf leaf) get() interface{} {
	if !lf.inList {
		return lf.parent[lf.key]
	}
	if arr, ok := lf.parent[lf.key].([]interface{}); ok && lf.index < len(arr) {
		return arr[lf.index]
	}
	return nil
}

// set replaces the addressed value if it is present.
func (lf leaf) set(v interface{}) {
	if !lf.inList {
		lf.parent[lf.key] = v
		return
	}
	if arr, ok := lf.parent[lf.key].([]interface{}); ok && lf.index < len(arr) {
		arr[lf.index] = v
	}
}

// drop deletes the addressed value: the map key, or the array element (later
// elements move up one).
func (lf leaf) drop() {
	if !lf.inList {
		delete(lf.parent, lf.key)
		return
	}
	arr, ok := lf.parent[lf.key].([]interface{})
	if !ok || lf.index >= len(arr) {
		return
	}
	out := make([]interface{}, 0, len(arr)-1)
	out = append(out, arr[:lf.index]...)
	lf.parent[lf.key] = append(out, arr[lf.index+1:]...)
}

// resolveLeaves returns every leaf addressed by steps, following array and map
// wildcards. Missing intermediate nodes yield no leaves (no-op paths).
func resolveLeaves(node interface{}, steps []pathStep) []leaf {
	if len(steps) == 0 {
		return nil
	}
	step := steps[0]
	rest := steps[1:]

	if step.mapWild {
		m, ok := node.(map[string]interface{})
		if !ok {
			return nil
		}
		out := make([]leaf, 0, len(m))
		for k := range m {
			out = append(out, leaf{parent: m, key: k})
		}
		return out
	}

	m, ok := node.(map[string]interface{})
	if !ok {
		return nil
	}
	if len(rest) == 0 && !step.array && !step.indexed {
		return []leaf{{parent: m, key: step.key}}
	}
	child, ok := m[step.key]
	if !ok {
		return nil
	}
	if step.indexed {
		arr, ok := child.([]interface{})
		if !ok || step.index >= len(arr) {
			return nil
		}
		if len(rest) == 0 {
			return []leaf{{parent: m, key: step.key, inList: true, index: step.index}}
		}
		return resolveLeaves(arr[step.index], rest)
	}
	if step.array {
		arr, ok := child.([]interface{})
		if !ok {
			return nil
		}
		if len(rest) == 0 {
			return []leaf{{parent: m, key: step.key}} // "a[*]" terminal → drop the whole array
		}
		var out []leaf
		for _, el := range arr {
			out = append(out, resolveLeaves(el, rest)...)
		}
		return out
	}
	return resolveLeaves(child, rest)
}

// dropParsed deletes every leaf addressed by steps.
func dropParsed(root map[string]interface{}, steps []pathStep) {
	for _, lf := range resolveLeaves(root, steps) {
		lf.drop()
	}
}

// PathMatches reports whether path (content.ignoreFields syntax) addresses at
// least one value present in body, so a caller can warn about an ignore path
// that never matches anything (usually a typo).
func PathMatches(body map[string]interface{}, path string) bool {
	for _, lf := range resolveLeaves(body, parsePath(path)) {
		if lf.exists() {
			return true
		}
	}
	return false
}

func dropAnnotations(body map[string]interface{}, exact, prefixes []string) {
	meta, ok := body["metadata"].(map[string]interface{})
	if !ok {
		return
	}
	annotations, ok := meta["annotations"].(map[string]interface{})
	if !ok {
		return
	}
	for _, key := range exact {
		delete(annotations, key)
	}
	for k := range annotations {
		for _, prefix := range prefixes {
			if strings.HasPrefix(k, prefix) {
				delete(annotations, k)
				break
			}
		}
	}
}

// dropEmptyMetadataMap removes metadata.<field> when stripping left it empty,
// so a normalized body doesn't carry vestigial "annotations: {}" blocks.
func dropEmptyMetadataMap(body map[string]interface{}, field string) {
	meta, ok := body["metadata"].(map[string]interface{})
	if !ok {
		return
	}
	if m, ok := meta[field].(map[string]interface{}); ok && len(m) == 0 {
		delete(meta, field)
	}
}
