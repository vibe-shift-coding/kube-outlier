# CLAUDE.md — kube-outlier

Read-only Go CLI that surfaces Kubernetes resources probably created by hand and never
cleaned up. The full design is in [docs/DESIGN.md](docs/DESIGN.md).

## Contract precedence

- Type shapes, package import rules, JSON schema, and exit codes are frozen in
  `docs/interfaces.md`. Never redefine a type inline; propose changes to that file
  explicitly.
- Settled decisions live in `docs/adr/`. Where `docs/DESIGN.md` prose conflicts with
  `interfaces.md` or an ADR, those files win.
- Planned work is tracked in `ROADMAP.md`.

## Hard rules

- **Strictly read-only.** No `Create`/`Update`/`Patch`/`Apply`/`Delete` on Kubernetes
  clients, ever. `scripts/check-readonly.sh` enforces this in CI.
- **Never print Secret values** — not in reports, `--why`, baselines, logs, or test
  output. The content comparison compares `sha256:` hashes internally and reports Secret
  digests only as per-run `hmac-sha256:` values. Kinds in `sensitive.kinds` get the same
  treatment for every value outside `apiVersion`, `kind` and `metadata` (ADR-0009).
- **Fail soft per cluster.** An unreachable cluster, forbidden type or API group that
  fails discovery is reported and skipped, never fatal.
- **Neutral wording.** Findings describe what is *unusual*, never what is *wrong*.
- Score weights live only in `internal/analyze/weights.go`; strip lists only in
  `internal/normalize/defaults.yaml`. Never inline heuristics.

## Conventions

- Conventional Commits; small, reviewable commits.
- Document every exported identifier.
- Context propagation and timeouts on every API call; no `context.TODO()` outside tests.
- Logs go to stderr via `log/slog`; stdout carries only the report.
- Keep goldens byte-identical unless the change is intended; regenerate deliberately
  with `go test ./internal/report -update`.

## Verify before finishing

```sh
go build ./... && go vet ./... && go test ./... && golangci-lint run ./... && ./scripts/check-readonly.sh
```
