# Roadmap

## v0.1 — first public release

Everything below already works:

- **Ownership signal** on a single cluster: Helm / Argo CD / Flux / controller /
  `kubectl` classification, `managedFields` evidence, transparent scoring.
- **Fleet presence signal** when two or more clusters are scanned: rare resources
  promoted, ones present everywhere demoted, `missing-from-cluster` findings.
- **Baselines**: a committable fleet fingerprint so one cluster can be checked against
  the fleet without live access to the others, with approved exceptions.
- **Content comparison** (`--compare-content`, 2+ clusters): field-by-field differences
  between copies of a shared resource, against the version most clusters run.
- `--why`; table, JSON, YAML and Markdown output; CI-friendly exit codes; a `version`
  command.
- Namespace scoping (`-n`/`--namespaces`), including for users whose access is limited
  to certain namespaces.

## Next

- [ ] Prebuilt binaries for Linux, macOS and Windows (amd64/arm64) via GoReleaser, shipping
  the third-party license notices with each archive
- [ ] Homebrew tap: `brew install vibe-shift-coding/tap/kube-outlier`
- [ ] krew plugin: `kubectl outlier`
- [ ] Ship the read-only `ClusterRole` / binding from [permissions.md](docs/permissions.md)
  as a file (`deploy/rbac.yaml`)
- [ ] CI: kind e2e across several Kubernetes versions; multi-cluster e2e
- [ ] Documentation site

## Later

- Namespace aliasing across clusters (e.g. `team-a-europe` ↔ `team-a-eu`)
- Fuzz corpus of anonymized real-world manifests for the normalizer
- "What changed since last time" comparisons between runs

Not provided today, to revisit on demand ([ADR-0010](docs/adr/0010-scope.md) says why
and what each needs first): manifest export, content hashes in baselines, saved scan
results for offline re-analysis, a finding for API types served on only part of the
fleet, and leaving cluster names out of baselines.

Ideas and requests are welcome as GitHub issues.
