# Running kube-outlier: a hands-on walkthrough

This walkthrough builds up every feature step by step against a local three-cluster
fleet: the ownership scan on one cluster, fleet comparison, baselines, `--why`,
content comparison, and scanning with namespace-only access. Each section's setup feeds the next, so
it's easiest to follow top to bottom.

> `kube-outlier` itself is read-only, but the setup steps in this walkthrough are not:
> they create and delete objects, including a `cluster-admin` ClusterRoleBinding
> (section 4) and the whole `demo-outliers` namespace. Run them only against a
> disposable test fleet such as the one below, never against clusters you care about.

**Before you start**

- Run every command from the repository root.
- Code blocks contain only commands, so they are safe to paste into bash or zsh.
  Explanations are in the text around them.
- `kube-outlier` reports through its **exit code** as well as its output — that is what
  CI checks. The shell doesn't print exit codes, so commands where it matters end with
  `; echo "exit code: $?"`, which adds a line such as `exit code: 3` after the output.
- Tools: Go, `kubectl` and `jq`; for the local fleet also `minikube` and a running
  Docker (see [SETUP.md](SETUP.md)). No Helm is needed.

## 1. Build and set up

Build the binary and make a scratch directory for the files this walkthrough writes
(kubeconfigs and baselines), so nothing lands in the repository:

```sh
go build -o kube-outlier ./cmd/kube-outlier
export DEMO=/tmp/kube-outlier-demo
mkdir -p "$DEMO"
```

If you open a new terminal later, re-run the `export DEMO=…` line.

## 2. Start a local fleet

`scripts/fleet-up.sh` starts three minikube clusters named `c1`, `c2` and `c3` in
Docker (see [SETUP.md](SETUP.md) for what it needs and does). It is safe to re-run.
`scripts/fleet-down.sh` stops them again.

```sh
scripts/fleet-up.sh
```

## 3. Connect: the `clusters` command

`clusters` resolves your kubeconfig contexts, probes each cluster, and reports its
server version. By default it uses the same kubeconfig and current context as
`kubectl`:

```sh
./kube-outlier clusters
```

Pick contexts explicitly, or use every context in the kubeconfig:

```sh
./kube-outlier clusters --contexts c1
./kube-outlier clusters --contexts c1,c2
./kube-outlier clusters --all-contexts
```

Expected: one row per cluster with `REACHABLE` = `yes` and its Kubernetes version
(yours may differ). A cluster's name is its context name:

```console
CLUSTER  REACHABLE  VERSION  NOTE
c1       yes        v1.35.1
c2       yes        v1.35.1
c3       yes        v1.35.1
```

`--context c1` (kubectl's spelling) works as well as `--contexts c1`.

An unreachable cluster still gets a row (`REACHABLE` = `no`, with a reason) and never
aborts the run. To try it, add the repository's test kubeconfig, whose one context
points at a port nothing listens on. (Stopping a cluster with `minikube stop` doesn't
work for this: minikube removes the stopped cluster's context, so the run fails with
`unknown context` instead.)

```sh
./kube-outlier clusters --kubeconfig "$HOME/.kube/config" --kubeconfig testdata/kubeconfig/unreachable.yaml --all-contexts; echo "exit code: $?"
```

Expected: rows for `c1`, `c2` and `c3`, plus `unreachable  no  -  Get "https://127.0.0.1:1/version…": … connection refused`,
then `exit code: 2`. If *no* cluster is reachable there is no table;
instead stderr lists each cluster with the reason it couldn't be reached. Exit codes for
`clusters`: `0` all reachable, `2` some unreachable, `1` none reachable or bad flags.

### One kubeconfig file per cluster

Real fleets often have one kubeconfig file per cluster. Repeat `--kubeconfig` to merge
them, exactly as `KUBECONFIG=a:b:c` would. To try it, split the local fleet into three
files:

```sh
for c in c1 c2 c3; do
  kubectl config view --minify --flatten --context="$c" > "$DEMO/$c.kubeconfig"
done
./kube-outlier clusters --kubeconfig "$DEMO/c1.kubeconfig" --kubeconfig "$DEMO/c2.kubeconfig" --kubeconfig "$DEMO/c3.kubeconfig" --all-contexts
```

The merge happens in memory using `kubectl`'s own rules (first file wins on clashing
context names, with a warning, and a context may use a user or cluster from another
file); nothing is written back to the files or to `~/.kube/config`. A file named with
`--kubeconfig` must exist: a typo is an error, not a smaller fleet.

Useful flags for `clusters`, `scan` and `baseline generate`: `--probe-timeout 10s` (how long to wait for each
cluster's reachability check, default `5s`), `--request-timeout` (every later request,
default `30s`), and `-v`/`-vv` for progress and timings on stderr. `scan` and `clusters`
also take `-o json|yaml|markdown`.

## 4. Scan one cluster (ownership signal)

With a single context, `scan` runs the ownership scan alone: pipeline markers,
`managedFields` evidence, kind and age.

### Set up: a pipeline-managed app and two hand-made one-offs

If you've run this walkthrough before, start from a clean slate (this deletes only the
`demo-outliers` namespace):

```sh
for c in c1 c2 c3; do
  kubectl --context "$c" delete namespace demo-outliers --ignore-not-found
done
```

First, a Deployment that looks exactly like a Helm release, on all three clusters. It is
applied with Helm's field manager and Helm's labels, which is how a real Helm install
looks to the API server, so no Helm or image downloads are needed. The scan must *not*
flag it.

```sh
for c in c1 c2 c3; do
kubectl --context "$c" create namespace demo-outliers
kubectl --context "$c" apply --server-side --field-manager=helm -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: demo-outliers
  labels:
    app.kubernetes.io/managed-by: Helm
  annotations:
    meta.helm.sh/release-name: web
    meta.helm.sh/release-namespace: demo-outliers
spec:
  replicas: 0
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: web
          image: nginx:1.27
EOF
done
```

Now two classic one-offs, on `c1` only. The ConfigMap is created with `kubectl apply`,
which leaves both a `last-applied-configuration` annotation and a `kubectl` field
manager. The NetworkPolicy is created with `kubectl create`, which leaves only the field
manager — still clear evidence that a person made it.

```sh
kubectl --context c1 apply -n demo-outliers -f - <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: debug-dump
data:
  note: "temp capture config for an incident, remove after RCA"
EOF
kubectl --context c1 create -n demo-outliers -f - <<'EOF'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: temp-allow-all
spec:
  podSelector: {}
  policyTypes: [Ingress]
  ingress:
    - {}
EOF
```

### Scan the cluster that has them

```sh
./kube-outlier scan --contexts c1 --fail-on inventory; echo "exit code: $?"
```

Expected — two findings, a summary line, then `exit code: 3`, which means "inventory
findings":

```console
SCORE  KIND                             NAMESPACE/NAME                CLUSTER  OWNER    AGE  WHY
75     networking.k8s.io/NetworkPolicy  demo-outliers/temp-allow-all  c1       kubectl  0d   no-pipeline-markers,kubectl-manager,kind-weight
70     ConfigMap                        demo-outliers/debug-dump      c1       kubectl  0d   no-pipeline-markers,kubectl-manager,last-applied
2 finding(s), 1 below the minimum score.
exit code: 3
```

The Helm-style `web` Deployment is the one below the minimum score: its markers keep its
score far below 40. The summary line goes to stderr, so it never ends up in redirected
output. In a terminal narrower than the table, each row's WHY codes move to an indented
`why:` line below it.

### Compare with a clean cluster

```sh
./kube-outlier scan --contexts c2; echo "exit code: $?"
```

Expected: just the table header, `No findings.` and `exit code: 0`.

### Only some namespaces

```sh
./kube-outlier scan --contexts c1 -n demo-outliers
```

The same two findings. With `-n`, each type is listed only inside the named namespaces,
which is also what makes namespace-only access work (section 9).

### Machine-readable output

```sh
./kube-outlier scan --contexts c1 -o json | jq '.findings[].resource'
```

The report goes to stdout and diagnostics to stderr, so the JSON pipes cleanly.

### Cluster-scoped resources

Cluster-scoped kinds such as `ClusterRoleBinding` are skipped unless you ask for them —
they're valuable (a hand-made `cluster-admin` binding is a classic leftover) but noisier.
Create one and scan with `--include-cluster-scoped`:

```sh
kubectl --context c1 create clusterrolebinding demo-debug-admin --clusterrole=cluster-admin --user=debug-user
./kube-outlier scan --contexts c1 --include-cluster-scoped
```

Expected: `rbac.authorization.k8s.io/ClusterRoleBinding  demo-debug-admin` (no
namespace), scoring 75 thanks to the high kind weight of RBAC bindings. You will also see the `demo-outliers` Namespace (you
created it with `kubectl create namespace`, so it is a hand-made object too) and, on
minikube, three objects minikube itself created with `kubectl` (`storage-provisioner`,
`minikube-rbac`, and the `standard` StorageClass) — genuine hand-applied resources as
far as the cluster can tell (see
[managed clusters](troubleshooting.md#managed-clusters-eks-gke-aks)). The hundreds of
objects Kubernetes creates itself (default roles, built-in APIServices, …) are
recognised and never reported; check one (status: `never reported: created by the
Kubernetes control plane`):

```sh
./kube-outlier scan --contexts c1 --include-cluster-scoped --why "rbac.authorization.k8s.io/ClusterRole admin"
```

Without `--include-cluster-scoped` the same `--why` exits `1` and says
`rbac.authorization.k8s.io/ClusterRole is cluster-scoped; add --include-cluster-scoped`.
Remove the binding again:

```sh
kubectl --context c1 delete clusterrolebinding demo-debug-admin
```

### Exit codes for `scan`

| Code | Meaning |
|---|---|
| `0` | Clean, or findings exist but `--fail-on` is `none` (the default) |
| `1` | Fatal: bad flags or config, unknown or duplicate context, no reachable cluster, baseline scope mismatch, a content gate that cannot run, a `--why` resource not found |
| `2` | Partial: a cluster, resource type or (with `--compare-content`) object couldn't be read and was skipped, nothing else triggered (`0` with `--allow-partial`) |
| `3` | `--fail-on inventory` (or `any`) and at least one `likely-manual` or `missing-from-cluster` finding |
| `4` | `--fail-on content` (or `any`) and at least one content difference; needs `--compare-content` (section 8) |

When several apply, the highest-priority one wins: `1 > 3 > 4 > 2 > 0`.
`--fail-on content` without `--compare-content`, or with fewer than two reachable
clusters (a baseline holds no content), exits `1`, since content would never be compared.

## 5. Scan the fleet (fleet presence signal)

Scanning two or more clusters together compares *where* each resource exists, and uses
that to adjust the ownership scores.

### Set up: a resource missing from one cluster, and one present everywhere

A Helm-style NetworkPolicy on `c1` and `c2` but not `c3` — the "never reached one
cluster" case:

```sh
for c in c1 c2; do
kubectl --context "$c" apply --server-side --field-manager=helm -f - <<'EOF'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny
  namespace: demo-outliers
  labels:
    app.kubernetes.io/managed-by: Helm
  annotations:
    meta.helm.sh/release-name: web
    meta.helm.sh/release-namespace: demo-outliers
spec:
  podSelector: {}
  policyTypes: [Ingress]
EOF
done
```

And an unlabelled Secret on all three, written by a non-`kubectl` tool, as a
bootstrap script or operator would:

```sh
for c in c1 c2 c3; do
kubectl --context "$c" apply --server-side --field-manager=bootstrap-script -f - <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: legacy-cert
  namespace: demo-outliers
type: Opaque
stringData:
  cert: dummy-not-a-real-secret
EOF
done
```

"Present on most clusters" means at least `analysis.commonThreshold` of them (default
0.6), so 2 of 3 counts.

### Scan all three

```sh
./kube-outlier scan --contexts c1,c2,c3 --fail-on inventory; echo "exit code: $?"
```

Expected — three findings, then `exit code: 3`:

```console
SCORE  KIND                             NAMESPACE/NAME                PRESENT-ON  CLUSTER  OWNER    AGE  WHY
95     networking.k8s.io/NetworkPolicy  demo-outliers/temp-allow-all  1/3         c1       kubectl  0d   no-pipeline-markers,kubectl-manager,kind-weight,fleet-rare
90     ConfigMap                        demo-outliers/debug-dump      1/3         c1       kubectl  0d   no-pipeline-markers,kubectl-manager,last-applied,fleet-rare
50     networking.k8s.io/NetworkPolicy  demo-outliers/default-deny    2/3         c3       -        0d   missing-from-cluster
3 finding(s), 8 below the minimum score.
exit code: 3
```

What changed compared with section 4:

- The two one-offs gained `fleet-rare` (+20): they exist on only 1 of 3 clusters.
- `default-deny` appears as `missing-from-cluster` against `c3`, the cluster that
  lacks it. `OWNER` is `-`: the finding is about the fleet, not one object.
- `legacy-cert` is *not* reported, although on its own it would be. Scan one cluster to
  see it: `./kube-outlier scan --contexts c2` shows it with score 50. Across the fleet
  it is present everywhere, which suggests a legitimate unlabelled resource, so
  `fleet-common` (−30) pushes it below the minimum score.

To turn off the missing-from-cluster findings, put `analysis.reportMissingFromCluster:
false` in a config file:

```sh
printf 'analysis:\n  reportMissingFromCluster: false\n' > "$DEMO/no-missing.yaml"
./kube-outlier scan --contexts c1,c2,c3 --config "$DEMO/no-missing.yaml"
```

## 6. Baselines: the fleet presence signal without the fleet

A baseline is a small file recording, for each resource, how many clusters had it and
how it was owned — never manifests or Secret values, though it does list cluster names
and each resource's kind, namespace and name. Generate it once from the fleet, commit it,
and later scans of a *single* cluster get the fleet presence signal from the file:

```sh
./kube-outlier baseline generate --contexts c1,c2,c3 --out "$DEMO/baseline.yaml"
```

Scan `c3` against it — the NetworkPolicy the fleet has but `c3` lacks shows up without
contacting `c1` or `c2`:

```sh
./kube-outlier scan --contexts c3 --baseline "$DEMO/baseline.yaml" --fail-on inventory; echo "exit code: $?"
```

Expected: one finding, `networking.k8s.io/NetworkPolicy demo-outliers/default-deny`,
`missing-from-cluster`, then `exit code: 3`. The presence column is titled `IN-BASELINE`
here: `2/3` means 2 of the baseline's 3 clusters. Scanning `c1` the same way reports its
two one-offs with `fleet-rare`, because the baseline saw them on only one cluster:

```sh
./kube-outlier scan --contexts c1 --baseline "$DEMO/baseline.yaml"
```

Things to know:

- **Scope must match.** The baseline records the exact scan scope (namespace and kind
  exclusions, `--include-cluster-scoped`, `-n`). A scan with a different scope stops
  with exit code `1` and a field-by-field diff; `--allow-scope-mismatch` turns that into
  a warning. `baseline generate` takes the same `-n` and `--include-cluster-scoped`
  flags as `scan`.
- **Exceptions.** Known-legitimate one-offs go in an `exceptions:` list you add at the
  top level of the file (each with `resource` in the `--why` format, group included,
  `reason` and `approvedBy`, optionally `expires` as a date such as `2027-01-01`).
  Matching findings are suppressed and counted in the summary line and in the JSON as
  `summary.suppressedByException`. Regenerating onto the same `--out` file keeps the
  list. See [usage.md](usage.md#exceptions) for an example.
- If a resource type can't be listed on every cluster (for example Secrets forbidden on
  one), it is left out of the baseline with a warning and `generate` exits `2`, so the
  ratios are never silently skewed.

## 7. Why, and output formats

### Why was this reported — or not?

```sh
./kube-outlier scan --contexts c1,c2,c3 --why "ConfigMap demo-outliers/debug-dump"
```

Shows every signal behind the score, the `managedFields` managers, creation time per
cluster, and the presence across the fleet. It works for resources that were *not*
reported, too — the status line says why:

```sh
./kube-outlier scan --contexts c1,c2,c3 --why "Secret demo-outliers/legacy-cert"
```

Expected status: `below the minimum score (score 20 < 40)`, with `fleet-common` in the
score breakdown. The identity format is `"<[group/]Kind> <[namespace/]name>"`,
for example `"apps/Deployment shop/api"` or
`"rbac.authorization.k8s.io/ClusterRole admin"`. Leave the group out and `--why` says
which spelling it probably meant:

```sh
./kube-outlier scan --contexts c1,c2,c3 --why "NetworkPolicy demo-outliers/temp-allow-all"; echo "exit code: $?"
```

Expected: `… not found in scope on any scanned cluster; did you mean
"networking.k8s.io/NetworkPolicy demo-outliers/temp-allow-all"?`, then `exit code: 1`.

The status names every finding the report shows for a resource, not only its own score:

```sh
./kube-outlier scan --contexts c1,c2,c3 --why "networking.k8s.io/NetworkPolicy demo-outliers/default-deny"
```

Expected status: `reported as missing-from-cluster: missing from c3 (present on 2/3 clusters)`,
followed by `not reported as likely-manual: below the minimum score (score 15 < 40)`: the
Helm-managed copies on `c1` and `c2` look pipeline-made, but `c3` lacks the policy.

### Output formats

`-o table|json|yaml|markdown` works on both `scan` and `clusters`. Markdown is handy for
pasting into an issue or pull request:

```sh
./kube-outlier scan --contexts c1 -o markdown
```

## 8. Compare content across clusters

This compares copies of the same resource *between clusters*, not the live
cluster differing from Git (that is Argo CD's and Flux's job). It compares *what* shared
resources contain. It is opt-in with `--compare-content` because it
reads full objects, it needs two or more reachable clusters, and it covers the kinds in
`content.kinds` (by default Deployments, ConfigMaps and Ingresses).

### Make one cluster differ

Roll `c1` back to an older image, as a manual hotfix or a stuck rollout would:

```sh
kubectl --context c1 apply --server-side --field-manager=helm -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: demo-outliers
  labels:
    app.kubernetes.io/managed-by: Helm
  annotations:
    meta.helm.sh/release-name: web
    meta.helm.sh/release-namespace: demo-outliers
spec:
  replicas: 0
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: web
          image: nginx:1.25
EOF
```

### Find it

```sh
./kube-outlier scan --contexts c1,c2,c3 --compare-content --fail-on content; echo "exit code: $?"
```

Expected: the findings from sections 4 and 5 again, plus a content-differs row for
`apps/Deployment demo-outliers/web` on `c1` (`PRESENT-ON` = `3/3`, `OWNER` = `-`,
`SCORE` = `-`, `WHY` = `content-differs`), and below the table:

```console
Content differences:

  apps/Deployment demo-outliers/web — majority version on 2 cluster(s): c2, c3
    cluster c1:
      spec.template.spec.containers[0].image: nginx:1.27 → nginx:1.25

Fields that differ most:
  spec.template.spec.containers[0].image differs on 1 cluster(s)

4 finding(s), 8 below the minimum score.
exit code: 4
```

The last line is `exit code: 4` (content differences). The one-offs from section 4 are still
listed in the table, but with `--fail-on content` only content differences affect the exit code; use
`--fail-on any` to fail on either.

How the comparison stays fair:

- Objects Kubernetes creates itself are never compared: each namespace's
  `kube-root-ca.crt`, for one, holds its own cluster's CA and always differs.
- A copy the comparison can't read (no `get` permission, a timeout) is reported on
  stderr and in the JSON `skippedObjects`, and the run exits `2`; it is never quietly
  treated as matching. If no copy can be read at all, `--fail-on content` exits `1`.
- Server-filled defaults (`schedulerName`, `terminationMessagePath`, …) are stripped,
  quantities compare by value (`100m` equals `0.1`), and empty equals absent — only real
  differences show. Add your own paths to ignore with `content.ignoreFields`; the paths
  the report prints, such as `spec.template.spec.containers[0].image`, work as they are
  (see [configuration](configuration.md#ignoring-fields-in-the-content-comparison)).
- Secrets are compared by hash, never by value, and reported only as digests keyed for
  that run (`hmac-sha256:…`), which can't be checked against a guessed value.
- The majority version is the one shared by more than half the clusters. When no group
  has a majority (a tie, or 2 of 5), the largest group is used and the report says
  "most common version (no majority)".

## 9. Namespace-only access

App teams often have read access only inside their own namespaces, granted by a
`RoleBinding`. A default scan lists every type across all namespaces, which such an
identity is forbidden to do; `-n` lists inside the named namespaces instead.

### Set up: a ServiceAccount that can read only `demo-outliers`

Its `Role` and `RoleBinding` are applied as Helm-managed, so they don't show up as
hand-made findings themselves:

```sh
kubectl --context c1 apply --server-side --field-manager=helm -f - <<'EOF'
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ns-reader
  namespace: demo-outliers
  labels: {app.kubernetes.io/managed-by: Helm}
  annotations: {meta.helm.sh/release-name: web, meta.helm.sh/release-namespace: demo-outliers}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ns-reader
  namespace: demo-outliers
  labels: {app.kubernetes.io/managed-by: Helm}
  annotations: {meta.helm.sh/release-name: web, meta.helm.sh/release-namespace: demo-outliers}
rules:
  - apiGroups: ["*"]
    resources: ["*"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ns-reader
  namespace: demo-outliers
  labels: {app.kubernetes.io/managed-by: Helm}
  annotations: {meta.helm.sh/release-name: web, meta.helm.sh/release-namespace: demo-outliers}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ns-reader
subjects:
  - kind: ServiceAccount
    name: ns-reader
    namespace: demo-outliers
EOF
```

Build a kubeconfig that signs in as that ServiceAccount (a short-lived token, in the
scratch directory):

```sh
SERVER=$(kubectl config view --raw --minify --context c1 -o jsonpath='{.clusters[0].cluster.server}')
kubectl config view --raw --minify --flatten --context c1 -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d > "$DEMO/c1-ca.crt"
TOKEN=$(kubectl --context c1 create token ns-reader -n demo-outliers)
export NSKC="$DEMO/ns-reader.kubeconfig"
kubectl config --kubeconfig "$NSKC" set-cluster c1 --server "$SERVER" --certificate-authority "$DEMO/c1-ca.crt" --embed-certs
kubectl config --kubeconfig "$NSKC" set-credentials ns-reader --token "$TOKEN"
kubectl config --kubeconfig "$NSKC" set-context c1-ns-reader --cluster c1 --user ns-reader
```

### Scan as that identity

Without `-n`, everything is forbidden at cluster scope:

```sh
./kube-outlier scan --kubeconfig "$NSKC" --contexts c1-ns-reader; echo "exit code: $?"
```

Expected: one line saying everything was forbidden, and what to do about it, then the
empty table, a note that nothing could be read, and `exit code: 2` (a partial run: things
were skipped):

```console
c1-ns-reader: all 23 resource types forbidden cluster-wide, skipped; if you can read only some namespaces, scan them with -n <namespace>
SCORE  KIND  NAMESPACE/NAME  CLUSTER  OWNER  AGE  WHY
No findings — nothing could be read (see the skipped types above).
exit code: 2
```

The number of types depends on your Kubernetes version.

With `-n`, the scan works normally:

```sh
./kube-outlier scan --kubeconfig "$NSKC" --contexts c1-ns-reader -n demo-outliers
```

Expected: `temp-allow-all`, `debug-dump` and `legacy-cert` (single cluster, so no fleet
demotion), nothing skipped. Naming a namespace the identity can't read is reported per
namespace rather than failing the run:

```sh
./kube-outlier scan --kubeconfig "$NSKC" --contexts c1-ns-reader -n demo-outliers,default; echo "exit code: $?"
```

Expected: the same three findings, one line for the namespace the identity can't read,
and `exit code: 2`:

```text
c1-ns-reader: all 23 resource types in namespace default forbidden, skipped; does the namespace exist, and can this identity read it?
```

## 10. Clean up

```sh
for c in c1 c2 c3; do
  kubectl --context "$c" delete namespace demo-outliers --ignore-not-found
done
rm -rf "$DEMO"
scripts/fleet-down.sh
```

`scripts/fleet-down.sh --delete` removes the minikube clusters entirely instead of just
stopping them.

## Automated version

`scripts/fleet-smoke.sh` runs the same scenarios (in namespaces of its own) against the
running fleet and asserts every expected result, then cleans up — `KEEP=1` keeps its
fixtures for poking around. It refuses to run unless `c1`, `c2` and `c3` are running
minikube profiles of those names.

## Permissions

- `clusters` needs only the discovery API (`/version`), which every authenticated user
  has.
- `scan` needs `list` on every resource type in scope — cluster-wide by default, or
  within the named namespaces with `-n`. Only `--compare-content` also needs `get`, on the
  `content.kinds` resources it reads in full.
- Anything that can't be listed is reported as `forbidden, skipped` (per namespace with
  `-n`) and never counted as "missing" from that cluster; the run exits `2`. A copy the
  content comparison can't `get` is reported the same way and left out of the comparison.

See [permissions.md](permissions.md) for a ready-made
read-only role.
