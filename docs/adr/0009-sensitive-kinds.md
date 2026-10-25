# ADR-0009: Sensitive kinds beyond Secret

## Context
A normal scan reads metadata only, so no resource's values reach it, and the one
annotation that can carry values (`kubectl.kubernetes.io/last-applied-configuration`) is
emptied for every kind at collection. One feature reads full objects: the content
comparison (`--compare-content`, for `content.kinds`). It recognises secrets by kind: a
core `Secret`'s `data` and `stringData` are hashed (ADR-0006). No other feature reads
full objects (ADR-0010).

Custom resources can hold credentials too, for example the external-secrets `Fake`
generator or an operator resource with an inline password. Their secret fields have no
fixed name, and a built-in list of such kinds would always be incomplete and could give
false confidence.

## Decision
A `sensitive.kinds` setting lists extra kinds, in KindRef syntax, whose values are
handled like a Secret's. The core `Secret` is always sensitive and cannot be removed;
the default list is empty.

For a listed kind other than the core Secret, every value outside `apiVersion`, `kind`
and `metadata` is treated as secret, keeping the structure (map keys and list lengths)
so field paths stay readable:

- **Content comparison:** each value is replaced by `sha256:` of its JSON encoding
  before hashing and diffing (`normalize.CanonicalizeForComparison` with
  `sensitive=true`), and reports show only the per-run `hmac-sha256:` digests
  (ADR-0006).

A listed kind takes this path even if it is named `Secret` in another API group; only
the core `Secret` keeps the `data`/`stringData` treatment.

## Consequences
Credentials in custom resources can be kept out of reports without the tool guessing
which fields hold them. The cost is that a listed kind's non-secret values are hidden
too, so its content differences show only as changed digests. Users must know which of
their kinds hold credentials; the docs say so next to `content.kinds`. Any feature that
reads full objects in future must apply this setting too.
