# Contributing to kube-outlier

Bug reports, ideas and pull requests are welcome. For anything larger than a small fix,
please open an issue first so we can agree on the approach.

Security issues: please follow [SECURITY.md](SECURITY.md) instead of opening an issue.

## Before you change anything

- [docs/interfaces.md](docs/interfaces.md) freezes the JSON schema, exit codes, type
  shapes and package import rules. A change there needs its own reviewed pull request
  that updates that file first.
- Settled decisions are in [docs/adr/](docs/adr/); [ADR-0007](docs/adr/0007-user-facing-naming.md)
  and [ADR-0008](docs/adr/0008-names-and-report-shape.md) cover naming.
- Hard rules: the tool is strictly read-only; it never prints or stores Secret values;
  one unreachable cluster or forbidden type is reported and skipped, never fatal; findings
  describe what is unusual, never what is wrong.
- Score weights live only in `internal/analyze/weights.go`; strip lists only in
  `internal/normalize/defaults.yaml`.

[CLAUDE.md](CLAUDE.md) holds the same rules in the form AI coding assistants read.

## Checks

Requires the Go version in [go.mod](go.mod) or later. Run the same checks as CI:

```sh
go build ./... && go vet ./... && go test ./... && golangci-lint run ./... && ./scripts/check-readonly.sh
```

Golden files under `testdata/golden/` are byte-compared. When a change to the output is
intended, regenerate them and review the diff:

```sh
go test ./internal/report -update
```

## End-to-end tests

`scripts/fleet-up.sh` starts three local minikube clusters named `c1`, `c2` and `c3`
(see [docs/SETUP.md](docs/SETUP.md)); `scripts/fleet-smoke.sh` runs every scenario against
them and cleans up. The smoke test **writes** to those clusters, and refuses to run
unless they are running minikube profiles of those names.

## Pull requests

- Conventional Commits (`feat:`, `fix:`, `docs:`, …); small, reviewable commits.
- Document every exported identifier (`golangci-lint` enforces it).
- Every API call takes a context; no `context.TODO()` outside tests.
- Logs go to stderr via `log/slog`; stdout carries only the report.
