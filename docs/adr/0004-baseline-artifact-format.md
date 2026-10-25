# ADR-0004: Baseline artifact — YAML fingerprint with mandatory provenance and strict scope matching

## Context
Baselines are a committable "portable fleet fingerprint". The design fixes the intent
(fingerprint only, provenance header, approved exceptions, refuse-or-warn on scope
mismatch) but not the format, the mismatch behavior, or how scope equality is computed.

## Decision

**Format: YAML** (via `sigs.k8s.io/yaml`, so it is JSON-compatible byte-for-byte in
structure). Default filename `baseline.yaml`; `--out file.json` emits JSON of the identical
structure. Rationale: baselines are meant to be committed and code-reviewed; YAML diffs
review better, and approved-exceptions will be hand-edited.

**Shape:** exactly the `baseline` package types in `docs/interfaces.md` §2.7
(`Provenance`, `Entry`, `ApprovedException`). `schemaVersion: 1`. Entries sorted by
`ResourceKey.String()` so regenerated baselines produce minimal VCS diffs. Never contains
manifests, spec bodies, content hashes or secret values — presence counts and ownership
tallies only.

**Layout:** `provenance`, then the hand-edited `exceptions`, then the generated
`entries`, in that order in both YAML and JSON. Resources are written as identity strings
(`apps/Deployment shop/api`, the same syntax `--why` takes); the object form
`{group, kind, namespace, name}` is also accepted when reading. A baseline holds no
content hashes (ADR-0010): a hash is only meaningful under the exact normalization,
`content.ignoreFields` and `sensitive.kinds` that produced it, and the fingerprint
records none of them.

**Scope fingerprint & validation:**
- `ScopeFingerprint.Hash` = sha256 hex of canonical JSON (sorted keys, no whitespace) of
  the fingerprint's other fields, which are themselves sorted lists in canonical KindRef
  syntax with built-in defaults merged in.
- `scan --baseline`: recompute the fingerprint for the *current* scan scope and compare
  hashes. Mismatch → **fatal, exit 1**, printing a field-by-field diff of the two
  fingerprints. `--allow-scope-mismatch` downgrades to a loud stderr warning (scan
  proceeds; exit code otherwise unaffected). Rationale: silent corruption of comparisons
  is the stated risk; strict-by-default with an explicit escape hatch beats a warn-only
  default that CI will ignore.
- Unknown/newer `schemaVersion` → fatal, exit 1. `toolVersion` skew alone is informational
  only.

**Approved exceptions:** matched by exact `ResourceKey`. `reason` and `approvedBy` are
required non-empty (validation error otherwise). `expires` is an RFC 3339 time or a plain
date (`2027-01-01`, midnight UTC). Expired entries (Expires < now) are ignored and
reported once on stderr. An active exception that names a resource neither the baseline
nor the scanned cluster has is reported on stderr, since it suppresses nothing. Suppressed findings appear in the JSON report summary
as `summary.suppressedByException` (additive schema change, still schemaVersion 1).

**Consumption:** with `--baseline`, the Tier 2 fleet presence signal uses `Provenance.ClusterCount` as
the fleet denominator and `Entry.Ratio`/`ClusterCount` as presence data
(`fleetPresence.source: "baseline"`); no live peers are contacted.

## Consequences
- Baselines are safe to commit, reviewable, and mechanically guarded against
  scope-mismatch misuse.
- Strict matching means changing scan scope requires regenerating the baseline — the
  correct workflow, made explicit.
- Adding fields to the fingerprint later changes hashes and invalidates old baselines;
  that is a schemaVersion bump by definition.
