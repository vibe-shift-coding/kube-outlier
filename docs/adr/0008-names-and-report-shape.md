# ADR-0008: Names for findings, signals, flags and settings; report shape

## Context
ADR-0007 names the two signals and the content comparison. The remaining user-facing
names — finding classes, signal and reason codes, owner rendering, flags, config keys and
JSON fields — follow the same priorities: unambiguous to someone reading them cold at a
terminal, neutral wording (findings describe what is unusual, never what is wrong), and
no collision with terms Kubernetes tooling already owns. Several report fields also need
a precise meaning for findings that describe absence rather than one
object.

## Decision

**Names**, with the alternatives considered and why they were not chosen:

| Name | Surface | Not chosen, and why |
|---|---|---|
| `likely-manual` | finding class | `suspected-manual`: "suspected" frames a finding as an accusation. |
| `no-pipeline-markers` | signal and WHY reason | `unmanaged`: next to the managedFields reason `kubectl-manager` it reads as a contradiction. |
| `fleet-common` | WHY reason | `fleet-ubiquitous`: plainer word, matches the "common" bucket and `commonThreshold`. |
| `kubectlWritten` | JSON | `humanApplied`: managedFields records the client, not the person, and `kubectl create`/`edit` are not "apply". |
| `kubectl`, `helm+kubectl`, … | OWNER column | A "(human!)" marker: reads as an alarm, and is redundant for owner `kubectl`. |
| `controller.*`, `argocd.*` | signal families | `ctrl.*`, `argo.*`: the families match the owner values. |
| `commonThreshold` | config | `majorityThreshold`: "majority" also names the content comparison's reference version. |
| `rareThreshold` | config | `outlierThreshold`: every finding is an outlier (ADR-0007); pairs with `fleet-rare`. |
| `minScore`, `--min-score` | config, flag | `reportThreshold`: a 0–100 score, unlike the two 0–1 ratio thresholds. |
| `fleetPresence` | JSON | `enrichment`: a design-document term. |
| `majorityValue` | JSON | `fleetValue`: the same object says `majorityHash` and `majorityClusters`. |
| `--why` | flag | `--explain`: `kubectl explain` owns the word (API field documentation); `--why` pairs with the WHY column. |
| `--discard-exceptions` | flag | `--no-carry-exceptions`: a double negative. |
| `Fields that differ most:` | report text | "Hot fields": jargon. |

Names chosen for features kube-outlier does not provide (ADR-0010), for use if one is
added: `api-missing-from-cluster` (not `api-asymmetry`: opaque when read cold),
`contentHash` (not `specHash`), `--save-inventory`/`--from-inventory`
(not `--snapshot`, which reads as an etcd or volume snapshot, nor `--record`/`--replay`:
`--record` is kubectl's flag for annotating a resource with the command),
`--omit-cluster-names` (not `--redact-cluster-names`: "redact" means a `<redacted>`
placeholder), `--hide-pipeline-owned` (not `--only-unmanaged`).

`--context` and `--namespace` are accepted as aliases for `--contexts` and `--namespaces`
(kubectl's spellings). `-o/--output` exists only on `scan` and `clusters`, the commands
that render a report.

**Report shape:**
- `missing-from-cluster` is reported when a resource's presence ratio is at least
  `commonThreshold` (default 0.6, so 2 of 3 clusters).
- `clusters` lists where the finding applies: for `missing-from-cluster`, where the
  resource is missing.
- `ownership` is omitted, and the OWNER column shows `-`, on findings that classify no
  single object. `ageDays` is omitted, and AGE shows `-`, when no creation time is known.
- `presence.presentCount` is always exact. In baseline mode the cluster name lists are
  empty, and the table titles the column `IN-BASELINE`.
- The content comparison calls its reference a majority only when it holds more than
  half the copies; otherwise `ambiguous` is set and the text says "most common version".
- The KIND column shows `group/Kind`, so a row pastes into `--why`.
- Baseline files write resources as identity strings and put `exceptions` above
  `entries`; YAML output keeps the JSON field order.
- `clusters -o json` is part of the contract (interfaces.md §3c).

## Consequences
Every name says what it describes and none borrows a Kubernetes term with another
meaning. Changing any of them, or the report shape above, needs a schemaVersion bump.
