# testdata/fixtures — ownership classification & golden-pipeline fixtures

Full manifests as exported from a cluster (metadata realistic, incl. `managedFields`).
Loaded by `internal/testutil.LoadFixtures`, which extracts GVK + metadata into
`inventory.Resource` (cluster name `fixture`). Test clock is frozen at
`2026-07-01T00:00:00Z`; changing any `creationTimestamp` here changes age scoring and
breaks goldens.

| file | classifies as | KubectlWritten | base score | finding? |
|------|---------------|--------------|-----------:|----------|
| helm-deployment.yaml            | helm       | no  | 18 | no |
| argocd-service.yaml             | argocd     | no  | 18 | no |
| controller-owned-configmap.yaml | controller | no  | —  | no (ownerReferences excluded) |
| kubectl-configmap.yaml          | kubectl    | yes | 80 | **yes** (#1) |
| kubectl-networkpolicy.yaml      | kubectl    | yes | 75 | **yes** (#2) |

Weights: ADR-0002. Marker rules: ADR-0005. The golden pipeline test
(`internal/report/golden_test.go`) must find exactly the two kubectl fixtures, in score
order 80 then 75.
