# Troubleshooting

**`forbidden, skipped` lines and exit code 2.** Your identity can't list those types.
`all 23 resource types forbidden cluster-wide` means it can't list anything across the
cluster: if you only have access to some namespaces, scan them with `-n`. `all … in
namespace X forbidden` means that namespace is off-limits or doesn't exist. For a few
types, exclude the ones you don't need, or pass `--allow-partial` if the skips are
expected. See [Permissions](permissions.md).

**`No findings.` but I expected some.** Run `--why "<[group/]Kind> <namespace>/<name>"`
on the resource you expected. It says why it wasn't reported (below the minimum score,
owned by a controller, a system object, or covered by a baseline exception). If it says
"not found in scope", the rest of the line names the cause when it can: a kind written
without its group (`did you mean "networking.k8s.io/NetworkPolicy …"?`), a cluster-scoped
kind without `--include-cluster-scoped`, or an excluded namespace (`kube-system`,
`kube-public` and `kube-node-lease` are excluded by default). With `-n`, check the
namespace names: a named namespace that holds nothing gets a
`nothing found in namespace … does it exist?` warning on stderr.

**A cluster shows as skipped.** It didn't answer the reachability probe in time
(`--probe-timeout`, default `5s`), or its API discovery failed or took longer than
`--request-timeout` (default `30s`). Check `kube-outlier clusters`, raise the timeout,
and use `-v` to see how long each step took. The other clusters are still scanned.

**`no answer within --probe-timeout` on a cluster that uses a login plugin.** Credential
plugins (`aws eks get-token`, `gke-gcloud-auth-plugin`, `kubelogin`, an SSO login) run
inside the reachability check, so a login that waits for you, or a slow first run of a
cloud CLI, looks like a slow cluster. Log in first, for example with
`kubectl --context <context> get --raw /version`, or raise `--probe-timeout`.

**`no Auth Provider found for name "gcp"` (or `"azure"`).** Those legacy kubeconfig auth
providers were removed from Kubernetes clients, kubectl included. Regenerate the
kubeconfig with your provider's exec plugin (`gke-gcloud-auth-plugin`, `kubelogin`). The
legacy `oidc` provider still works.

**`… skipped: … Client.Timeout exceeded` lines and exit code 2.** Listing those types
took longer than `--request-timeout`; only those types are skipped, not the cluster.
Raise `--request-timeout`, or exclude the types if you don't need them.

**`get … forbidden, left out of the content comparison` and exit code 2.** Your
identity can `list` that type but not `get` it, and the content comparison reads full
objects. Grant `get` on the `content.kinds` you compare (see
[Permissions](permissions.md)), or narrow `content.kinds`. If no copy at all can be read,
`--fail-on content` exits `1` instead, since the gate could never fail.

**`<group>/<version>/* skipped: discovery failed` and exit code 2.** An aggregated API
(served by an add-on through an `APIService`) is registered but not answering, so the
types it serves couldn't be discovered. Fix or remove the `APIService`, or pass
`--allow-partial` if it is expected. The metrics APIs (`metrics.k8s.io` and the custom
and external metrics APIs) are exempt: their types are never scanned.

**`baseline scope mismatch` when using a baseline.** The baseline was generated with
different exclusions, `-n` namespaces or `--include-cluster-scoped` than this run; the
lines above the error show the difference (`+` only in this scan, `-` only in the
baseline). Regenerate it with the same config and flags (`baseline generate` takes `-n`
and `--include-cluster-scoped` too), or pass `--allow-scope-mismatch` to proceed with a
warning.

**`matches no resource type on any scanned cluster`.** A kind in `scope.kinds`,
`content.kinds` or `sensitive.kinds` names nothing the clusters serve, usually because the
group is missing (`Deployment` instead of `apps/Deployment`); the warning suggests the
spelling. Without a matching `content.kinds` entry, `--fail-on content` exits `1`.
`content.ignoreFields: … matched no field in any compared copy` is the same for a field
path; see [Ignoring fields](configuration.md#ignoring-fields-in-the-content-comparison).

**`unknown setting(s)` or `unknown environment variable(s)`.** A key in the config file,
or a `KUBE_OUTLIER_*` variable, names no setting — usually a typo. The message names it;
the valid settings are listed in [configuration](configuration.md).

**Everything I deployed with my pipeline shows owner `kubectl`.** Your pipeline runs
`kubectl apply`, and `managedFields` records the client, not the person. Pipeline
markers (Helm, Argo CD, Flux labels) and, with several clusters, fleet presence are what
separate pipeline resources from hand-made ones. If neither applies, exclude the
namespaces your pipeline owns, or record the known resources as
[baseline exceptions](usage.md#exceptions). Raising
`--min-score` is not a good fix: real hand-made resources score in the same range.

## Managed clusters (EKS, GKE, AKS)

With `--include-cluster-scoped`, expect a few findings for objects the provider or
installer created with `kubectl` — on minikube, for example, the `storage-provisioner`
and `minikube-rbac` ClusterRoleBindings and the `standard` StorageClass. As far as the
API server can tell they are hand-applied, so they are reported. Objects the control
plane creates itself (default RBAC, built-in APIServices, …) are recognized and never
reported.

To quiet them:

- Scan several clusters: provider objects exist everywhere, so `fleet-common` (−30)
  lowers their score. Objects written with `kubectl apply` usually stay above the
  minimum even so (on minikube, 61, 51 and 46 across three clusters), so this mainly
  orders them below real one-offs.
- Record them as [baseline exceptions](usage.md#exceptions).
- Leave `--include-cluster-scoped` off for routine scans and use it for periodic reviews.

Some providers also install add-ons in namespaces of their own outside `kube-system`. If
those show up in routine scans, add the namespaces to `scope.namespaces.exclude`.

Which objects appear depends on the provider and its version; this has been checked on
minikube only. Login plugins and large clusters are covered above and in
[Load on the API server](usage.md#load-on-the-api-server).

Still stuck? Ask in [Discussions](https://github.com/vibe-shift-coding/kube-outlier/discussions),
or [open an issue](https://github.com/vibe-shift-coding/kube-outlier/issues) with the
command you ran and its output (remove anything sensitive first).
