# ADR-0001: Structural diffs via wI2L/jsondiff (RFC 6902), not dyff

## Context
Tier 3 (the content comparison) needs a semantic diff of normalized objects. Three
candidates were considered: dyff-as-a-library, jsondiff/json-patch producing RFC 6902
ops with our own rendering, or go-cmp. dyff is primarily a CLI; its library API is not a
stability contract, it pulls a large dependency tree, and its human rendering would fight
our own report layer. We already normalize to canonical JSON before comparing, so a
JSON-native differ is a natural fit.

## Decision
- Use `github.com/wI2L/jsondiff` to diff normalized canonical-JSON objects, producing
  RFC 6902 JSON Patch operations.
- `internal/report` renders ops as `path: majorityValue → clusterValue` lines; the raw ops
  array also goes verbatim into the JSON report (`content-differs` findings).
- `github.com/google/go-cmp` is allowed in `_test.go` files only — never in shipped code,
  never for user-facing output.
- dyff is rejected as a dependency in any form.

## Consequences
- Full control of rendering; diff ops are machine-consumable in CI via the JSON report.
- We own the human-readability work (path prettifying like
  `spec.template.spec.containers[0].image`). Acceptable: small and testable with golden
  files.
- Revisit only if jsondiff becomes unmaintained; the RFC 6902 output contract would
  survive a library swap.
