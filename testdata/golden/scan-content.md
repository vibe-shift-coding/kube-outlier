## kube-outlier scan

`v0.1.0-test` — generated 2026-07-01 00:00:00 UTC

| SCORE | KIND | NAMESPACE/NAME | PRESENT-ON | CLUSTER | OWNER | AGE | WHY |
| --- | --- | --- | --- | --- | --- | --- | --- |
| - | apps/Deployment | default/web | 3/3 | c | - | 30d | content-differs |

1 finding(s), 0 below the minimum score.

```
Content differences:

  apps/Deployment default/web — majority version on 2 cluster(s): a, b
    cluster c:
      spec.template.spec.containers[0].image: web:v1 → web:v2

Fields that differ most:
  spec.template.spec.containers[0].image differs on 1 cluster(s)
```
