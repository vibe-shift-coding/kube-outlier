# ADR-0006: Content-comparison canonicalization and majority-version rules

## Context
The content comparison (Tier 3) compares the same resource across clusters. DESIGN.md
§7/§8 fix the intent — normalize, group copies into a majority version, diff the
minorities — but leave the exact canonicalization and majority rules open. Left
unfixed, results would depend on which fields happen to be stripped, how copies are
hashed and how ties are broken, and would not be reproducible. ADR-0001 decides the
differ (`wI2L/jsondiff`, RFC 6902); this ADR fixes the normalization and majority rules
that feed it.

## Decision

**Two normalization layers.** `normalize.Normalize` stays conservative: it drops only
unambiguous server bookkeeping. The content comparison uses
`normalize.CanonicalizeForComparison`, which layers stricter rules on top.
Over-stripping is acceptable for comparison (it only hides fields both sides agree on or
that are pure server noise) but would be wrong for anything that must stay a complete
manifest, hence the split.

**Aggressive server-default strips** (comparison only, data-driven `comparisonPerKind`
in `normalize/defaults.yaml`): pod-template `terminationMessagePath`,
`terminationMessagePolicy`, `imagePullPolicy`, `dnsPolicy`, `schedulerName`,
`restartPolicy`, and empty `securityContext`. These are server-populated or
environment-specific and would swamp real differences. `containers[*].resources` is
kept — resource differences matter.

**Quantity canonicalization.** At declared `quantityPaths` (e.g. container
`resources.limits.*`/`requests.*`), values are parsed with `resource.ParseQuantity` and
replaced by the canonical `.String()`, so `100m`==`0.1` and `1Gi`==`1024Mi` hash equal.
Unparseable values are left as-is (never error).

**Empty ⇔ nil.** After strips, empty maps `{}` and empty slices `[]` are removed
recursively before hashing, so `{}` and an absent field compare equal. Comparison only —
`Normalize` alone keeps them.

**Array order preserved.** Arrays (notably container `env`) are never sorted — order can
carry meaning (env var references resolve top-to-bottom). Reordered env is a real
difference.

**Secrets compared as hashes, never values.** In the content comparison a Secret's
`data`/`stringData` values are replaced by `sha256:<hex>` before canonicalization
(`HashSecretData`). Identical secrets across clusters hash equal (no false difference);
different ones diff as `hash → hash`. No plaintext ever enters a diff or the JSON report.

**Secret digests are keyed before reporting.** A plain SHA-256 of a short Secret value
is easily reversed by hashing guesses, so before a report is built every Secret digest
(value hashes in field ops, and the whole-body hashes) is re-keyed with HMAC-SHA256 under
a random key generated for that run: equal values still match within one report, and
nothing can be checked against a guess.

**Majority version = largest hash group.** For an identity, group clusters by
`CanonicalHash`; the group with the most clusters holds the majority version. Ties (≥2
groups of equal largest size) are broken deterministically — the group whose sorted
cluster-name list is lexicographically first wins — and the finding sets
`ContentDetail.Ambiguous=true` and says so in its message. `Ambiguous` is also set when
the largest group holds no more than half the copies (2/1/1/1 on five clusters): that is
the most common version, not a majority, and the report says so. An identity where all
clusters share one hash produces no finding.

**Every copy is accounted for.** Only `content.kinds` resources that are not system
objects (ADR-0005) are fetched. A copy that cannot be read (a forbidden `get`, a
timeout) is left out of the comparison and reported in the JSON `skippedObjects` and on
stderr, and the run is partial (exit 2): a silently missing copy could hide a
difference. If none of the copies could be read, the comparison did not run, and
`--fail-on content` is fatal (exit 1). A copy deleted since it was listed is simply
gone and is not reported.

**Live clusters only.** The comparison needs two or more reachable clusters. Baselines
hold no content hashes (ADR-0010): a stored hash could show only *that* content differs,
never which field, and any change to this ADR's strip lists, `content.ignoreFields` or
`sensitive.kinds` would invalidate it, none of which a baseline records.

## Consequences
- Results are reproducible: the same fleet state yields the same majority version,
  hashes and ops regardless of run or cluster iteration order.
- Tuning the strip/quantity lists touches `normalize/defaults.yaml` + this ADR only.
- `jsondiff.Compare` runs with no options (factorization off), so only add/remove/replace
  ops are produced; `OldValue` is populated by default and maps directly to
  `FieldOp.MajorityValue`.
- Revisit the aggressive strip list as real-world manifests surface new server-default
  noise.
