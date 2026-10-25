## kube-outlier scan

`v0.1.0-test` — generated 2026-07-01 00:00:00 UTC

| SCORE | KIND | NAMESPACE/NAME | PRESENT-ON | CLUSTER | OWNER | AGE | WHY |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 100 | ConfigMap | default/debug-dump | 1/3 | a | kubectl | 137d | no-pipeline-markers, kubectl-manager, last-applied, age, fleet-rare |
| 50 | apps/Deployment | default/ingress | 2/3 | c | - | 231d | missing-from-cluster |

2 finding(s), 5 below the minimum score.
