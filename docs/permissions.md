# Permissions

By default `kube-outlier` lists each resource type **across all namespaces at once**,
which needs cluster-wide read access. With `-n`/`--namespaces` it lists only inside the
namespaces you name, so read access within those namespaces is enough.

- `list` on every resource type in scope — cluster-wide (via a `ClusterRoleBinding`), or
  per namespace when using `-n` (a `RoleBinding` is fine) — plus API discovery (granted
  to all authenticated users by default). That is all a scan needs.
- `get` on the `content.kinds` resources, only if you use `--compare-content`, which
  reads them in full. Without it, each copy it can't read is reported (`… forbidden, left
  out of the content comparison`) and the run exits `2`; if it can read none,
  `--fail-on content` exits `1` rather than pass.

Listing uses the metadata API, so with `list` on Secrets the tool receives names, labels
and annotations, not the `data` fields. One annotation can still carry values:
`kubectl apply` stores the whole applied manifest in
`kubectl.kubernetes.io/last-applied-configuration`. `kube-outlier` keeps only the fact that
this annotation exists and drops its value as soon as it is read, so it never reaches a
report or a baseline.

## Check your access

The surest check is a scan itself: it never fails on a missing permission, and names
each type it couldn't read on stderr as `… forbidden, skipped`.

To check beforehand, list your rules in a namespace you will scan:

```sh
kubectl auth can-i --list -n team-a
```

A row with Resources `*.*` and Verbs including `get` and `list` means you can read every
type in every API group in that namespace, custom resources included; the
[read-only role](#a-minimal-read-only-role) below grants exactly that. A row with a bare
`*` covers only the core API group (ConfigMaps, Secrets, Services, …).

`--list` answers for one namespace, so it can't confirm cluster-wide access. For a scan
without `-n`, spot-check a few types from different API groups across all namespaces:

```sh
kubectl auth can-i list deployments.apps --all-namespaces
kubectl auth can-i list rolebindings.rbac.authorization.k8s.io --all-namespaces
kubectl auth can-i list secrets --all-namespaces
```

Avoid `kubectl auth can-i list '*'`: kubectl reads a bare `*` as every resource in the
**core** API group only, so it answers `yes` even when Deployments, RBAC objects and
custom resources are forbidden.

## With less than full access

The tool never fails because of a missing permission. Each resource type it can't list
is skipped, reported on stderr, and the run exits `2` (partial):

```text
cluster-eu1: v1/secrets forbidden, skipped
cluster-eu1: v1/secrets in namespace team-a forbidden, skipped
```

The second form appears with `-n`. When every type is forbidden, one line says so, with
the likely fix:

```text
cluster-eu1: all 23 resource types forbidden cluster-wide, skipped; if you can read only some namespaces, scan them with -n <namespace>
cluster-eu1: all 23 resource types in namespace team-b forbidden, skipped; does the namespace exist, and can this identity read it?
```

If nothing at all could be read, the table ends with `No findings — nothing could be
read`, not a plain `No findings.`.

What that means in practice:

| Your access | Result |
|---|---|
| Cluster-wide read on everything | Full scan. |
| Cluster-wide read on most types (e.g. no Secrets, or no access to some custom resource types) | Everything else is scanned normally; the missing types are skipped. A cluster that couldn't list a type is never counted as "missing" that resource in fleet comparisons. |
| Read access only inside certain namespaces (`RoleBinding`s) | Use `-n ns1,ns2`: each namespace is listed separately and scanned normally, and the run exits `0`. Without `-n`, every type is forbidden at cluster scope, so the scan reads nothing, says so, and exits `2`. Cluster-scoped types (with `--include-cluster-scoped`) are always listed cluster-wide, so a namespace-only identity gets them skipped as forbidden. |

If some types are deliberately off-limits, exclude them so runs exit `0` instead of `2`:

```yaml
scope:
  kinds:
    exclude: [Secret]
```

Alternatively, `--allow-partial` keeps reporting the skips but exits `0` for them.

## A minimal read-only role

The simplest option is to grant read access to everything, as below. Be aware of what
that allows: anyone using this identity can read Secret values with any tool, for example
`kubectl get secret -o yaml`. `kube-outlier` itself never shows or stores Secret values —
a normal scan reads only metadata, and `--compare-content` reduces Secret values to
per-run digests. If that access is too broad, see
[A role that can't read Secrets](#a-role-that-cant-read-secrets).

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kube-outlier-reader
rules:
  - apiGroups: ["*"]
    resources: ["*"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kube-outlier-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kube-outlier-reader
subjects:
  - kind: Group                 # or User / ServiceAccount
    name: platform-auditors
    apiGroup: rbac.authorization.k8s.io
```

Without `--compare-content`, `verbs: ["list"]` is enough. For a team that should only see
its own namespaces, put the same `rules` in a `Role` and bind it with a `RoleBinding` in
each namespace, then scan with `-n`.

The built-in `view` ClusterRole is *not* enough on its own: it can't read RBAC objects
(`Role`, `RoleBinding`, …), which are among the most important things to check.

## A role that can't read Secrets

Kubernetes RBAC can't grant metadata-only access: `list` on Secrets also returns their
values to any other tool using the same identity, and RBAC has no "everything except"
rule. To keep Secrets out of reach, list the core-group types explicitly, leaving out
`secrets`, and grant the other API groups whole:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kube-outlier-lister
rules:
  # Core API group: every type kube-outlier scans, except Secrets.
  - apiGroups: [""]
    resources: [configmaps, limitranges, namespaces, persistentvolumeclaims, persistentvolumes,
                podtemplates, replicationcontrollers, resourcequotas, serviceaccounts, services]
    verbs: [list]
  # Built-in API groups, which hold no Secret values. Add the API groups of
  # your custom resources here.
  - apiGroups: [admissionregistration.k8s.io, apiextensions.k8s.io, apiregistration.k8s.io,
                apps, autoscaling, batch, certificates.k8s.io, coordination.k8s.io,
                discovery.k8s.io, events.k8s.io, flowcontrol.apiserver.k8s.io,
                networking.k8s.io, node.k8s.io, policy, rbac.authorization.k8s.io,
                resource.k8s.io, scheduling.k8s.io, storage.k8s.io]
    resources: ["*"]
    verbs: [list]
```

Bind it as above, and exclude Secrets from the scan so the run doesn't report them as
skipped and exit `2`:

```yaml
scope:
  kinds:
    exclude: [Secret]
```

Custom resources can hold credentials too (an operator resource with an inline password,
for example); leave their API groups out of the role, or list them in
[`sensitive.kinds`](configuration.md#sensitive-kinds) if you compare their content. With
this role, `--compare-content` also needs `get` on the `content.kinds` types.
