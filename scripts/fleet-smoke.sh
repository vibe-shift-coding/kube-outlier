#!/usr/bin/env bash
# End-to-end fleet smoke test (the fleet integration test in
# docs/DESIGN.md) against the local c1/c2/c3 fleet from
# scripts/fleet-up.sh. Requires no helm: "pipeline-managed" fixtures are
# created with `kubectl apply --server-side --field-manager=helm` plus the
# real Helm labels/annotations, which is classification-identical under
# ADR-0005 (owner helm via helm.manager + markers, KubectlWritten false) while
# needing no chart downloads or image pulls.
#
# What it sets up (namespace demo-fleet):
#   all clusters : Deployment web (helm-managed, replicas 0)
#                  Secret legacy-cert (un-annotated, manager bootstrap-operator)
#   c1, c2 only  : NetworkPolicy default-deny (helm-managed)  -> missing from c3
#   c1 only      : ConfigMap debug-dump   (kubectl apply)     -> rare one-off
#                  NetworkPolicy temp-allow-all (kubectl create) -> rare one-off
#   namespace leg: c1 namespace demo-fleet-other with ConfigMap stray-cm, and a
#                  ServiceAccount ns-reader that can read ONLY demo-fleet
#
# What it asserts:
#   fleet scan (default config): exit 3; the two one-offs found with
#     fleet-rare; default-deny reported missing-from c3; legacy-cert demoted
#     by fleet-common (absent); web absent.
#   single-cluster scan of c2: exit 3 with exactly legacy-cert (proving the
#     same resource IS a finding without fleet context), fleet presence not applied.
#   namespace leg: -n restricts listing to the named namespaces, overrides the
#     built-in exclusions, and works for an identity with only namespace-scoped
#     RBAC; env vars for camelCase keys are honoured.
#   why leg: --why explains a reported one-off and a missing-from-cluster
#     finding the same way the report does.
#   content leg: a differing image is a content-differs finding (exit 4); a
#     content gate that cannot run (one cluster) is fatal (exit 1).
#   list-only leg: copies the content comparison may not get are reported and
#     make the run partial (exit 2); with none readable, a content gate is
#     fatal (exit 1).
#   secret leg: a kubectl-applied Secret's value (copied into the
#     last-applied annotation) never reaches a report, --why or a log.
#   discovery leg: an aggregated API that is down is a skipped type (exit 2).
#
# KEEP=1 keeps the demo-fleet namespaces around for manual poking.
#
# WARNING: this script WRITES to whatever the contexts c1, c2 and c3 point at
# (it creates objects, deletes the demo-fleet namespaces, and creates and
# deletes a broken APIService). Run it only against the disposable fleet from
# scripts/fleet-up.sh.
set -euo pipefail
cd "$(dirname "$0")/.."

PROFILES=(c1 c2 c3)
NS=demo-fleet
OTHER_NS=demo-fleet-other
SECRET_NS=demo-fleet-secret
BROKEN_APISERVICE=v1.broken.kube-outlier-smoke.test

for tool in kubectl go python3; do
  command -v "$tool" >/dev/null || { echo "fleet-smoke: $tool not found" >&2; exit 1; }
done
# Refuse to write anywhere but the local minikube fleet: each context must be
# a running minikube profile of the same name whose only node is named after it.
command -v minikube >/dev/null || { echo "fleet-smoke: minikube not found; this test only runs against the fleet from scripts/fleet-up.sh" >&2; exit 1; }
for p in "${PROFILES[@]}"; do
  minikube -p "$p" status >/dev/null 2>&1 ||
    { echo "fleet-smoke: minikube profile $p is not running — run scripts/fleet-up.sh first" >&2; exit 1; }
  node=$(kubectl --context "$p" get nodes -o jsonpath='{.items[*].metadata.name}' 2>/dev/null) ||
    { echo "fleet-smoke: cluster $p unreachable — run scripts/fleet-up.sh first" >&2; exit 1; }
  [ "$node" = "$p" ] ||
    { echo "fleet-smoke: context $p points at a cluster with node(s) '$node', not the minikube profile $p; refusing to write to it" >&2; exit 1; }
done

echo "fleet-smoke: building kube-outlier..."
go build -o kube-outlier ./cmd/kube-outlier

WORK=$(mktemp -d)
cleanup() {
  if [ "${KEEP:-0}" != "1" ]; then
    for p in "${PROFILES[@]}"; do
      kubectl --context "$p" delete namespace "$NS" "$OTHER_NS" "$SECRET_NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true
    done
  else
    echo "fleet-smoke: KEEP=1 — leaving namespaces $NS, $OTHER_NS and $SECRET_NS in place"
  fi
  kubectl --context c1 delete apiservice "$BROKEN_APISERVICE" --ignore-not-found >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

# --- fixtures -----------------------------------------------------------

helm_managed_web() { # Deployment: helm-managed, no pods (replicas 0)
  cat <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: $NS
  labels:
    app.kubernetes.io/managed-by: Helm
    app.kubernetes.io/instance: web
  annotations:
    meta.helm.sh/release-name: web
    meta.helm.sh/release-namespace: $NS
spec:
  replicas: 0
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec: {containers: [{name: web, image: nginx}]}
EOF
}

helm_managed_default_deny() { # NetworkPolicy: helm-managed, the missing-from candidate
  cat <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny
  namespace: $NS
  labels:
    app.kubernetes.io/managed-by: Helm
    app.kubernetes.io/instance: web
  annotations:
    meta.helm.sh/release-name: web
    meta.helm.sh/release-namespace: $NS
spec:
  podSelector: {}
  policyTypes: [Ingress]
EOF
}

unannotated_secret() { # Secret: no markers, non-kubectl manager -> base 50
  cat <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: legacy-cert
  namespace: $NS
type: Opaque
stringData:
  cert: dummy-not-a-real-secret
EOF
}

echo "fleet-smoke: applying baseline to all clusters..."
for p in "${PROFILES[@]}"; do
  kubectl --context "$p" create namespace "$NS" --dry-run=client -o yaml |
    kubectl --context "$p" apply --server-side --field-manager=bootstrap-operator -f - >/dev/null
  helm_managed_web | kubectl --context "$p" apply --server-side --field-manager=helm -f - >/dev/null
  unannotated_secret | kubectl --context "$p" apply --server-side --field-manager=bootstrap-operator -f - >/dev/null
done
for p in c1 c2; do # deliberately NOT c3 -> missing-from-cluster
  helm_managed_default_deny | kubectl --context "$p" apply --server-side --field-manager=helm -f - >/dev/null
done

echo "fleet-smoke: injecting hand-applied one-offs into c1..."
kubectl --context c1 apply -n "$NS" -f - >/dev/null <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: debug-dump
data:
  note: "temp capture config for an incident, remove after RCA"
EOF
kubectl --context c1 create -n "$NS" -f - >/dev/null <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: temp-allow-all
spec:
  podSelector: {}
  policyTypes: [Ingress]
  ingress: [{}]
EOF

# --- scans --------------------------------------------------------------

# The later legs load a config file too, so that path stays exercised. Its
# setting equals the default.
cat > "$WORK/cfg.yaml" <<EOF
analysis:
  commonThreshold: 0.6
EOF

echo "fleet-smoke: fleet scan (c1,c2,c3), default config..."
set +e
./kube-outlier scan --contexts c1,c2,c3 --fail-on inventory -o json > "$WORK/fleet.json"
fleet_code=$?
echo "fleet-smoke: single-cluster scan (c2, via the kubectl-style --context alias)..."
./kube-outlier scan --context c2 --fail-on inventory -o json > "$WORK/single.json"
single_code=$?
./kube-outlier scan --contexts c2 -o yaml > "$WORK/single.yaml"
set -e
head -1 "$WORK/single.yaml" | grep -qx "schemaVersion: 1" ||
  { echo "fleet-smoke: FAIL — YAML report must start with schemaVersion" >&2; exit 1; }

python3 - "$WORK/fleet.json" "$fleet_code" "$WORK/single.json" "$single_code" <<'PY'
import json, sys

fleet = json.load(open(sys.argv[1])); fleet_code = int(sys.argv[2])
single = json.load(open(sys.argv[3])); single_code = int(sys.argv[4])
NS = "demo-fleet"
failures = []

def check(cond, msg):
    if not cond:
        failures.append(msg)

def find(rep, cls, kind, name):
    return [f for f in rep["findings"]
            if f["class"] == cls and f["resource"]["kind"] == kind and f["resource"]["name"] == name]

def reasons(f):
    return [r["code"] for r in f["score"]["reasons"]]

# Fleet scan
check(fleet_code == 3, f"fleet scan exit = {fleet_code}, want 3")
check(fleet["fleetPresence"] == {"applied": True, "source": "live"},
      f"fleet fleetPresence = {fleet['fleetPresence']}")

for kind, name in [("ConfigMap", "debug-dump"), ("NetworkPolicy", "temp-allow-all")]:
    fs = find(fleet, "likely-manual", kind, name)
    check(len(fs) == 1, f"{kind} {name}: want 1 likely-manual finding, got {len(fs)}")
    if fs:
        f = fs[0]
        check("fleet-rare" in reasons(f), f"{kind} {name}: want fleet-rare, got {reasons(f)}")
        check("no-pipeline-markers" in reasons(f), f"{kind} {name}: want no-pipeline-markers, got {reasons(f)}")
        check(f["ownership"]["kubectlWritten"], f"{kind} {name}: want kubectlWritten")
        check(f["clusters"] == ["c1"], f"{kind} {name}: clusters = {f['clusters']}")
        check(f["presence"]["presentOn"] == ["c1"] and f["presence"]["presentCount"] == 1
              and f["presence"]["fleetSize"] == 3, f"{kind} {name}: presence = {f['presence']}")

# The default commonThreshold (0.6) must report 2 of 3 with no config file.
miss = find(fleet, "missing-from-cluster", "NetworkPolicy", "default-deny")
check(len(miss) == 1, f"default-deny: want 1 missing-from-cluster finding with defaults, got {len(miss)}")
if miss:
    check(miss[0]["clusters"] == ["c3"], f"default-deny: clusters = {miss[0]['clusters']}")
    check(miss[0]["score"]["total"] == 50, f"default-deny: score = {miss[0]['score']['total']}")
    check("ownership" not in miss[0], f"default-deny: no object was classified, ownership = {miss[0].get('ownership')}")

check(not find(fleet, "likely-manual", "Secret", "legacy-cert"),
      "legacy-cert must be demoted by fleet-common in the fleet scan")
check(not [f for f in fleet["findings"] if f["resource"]["name"] == "web"],
      "helm-managed web must never be a finding")
check("dummy-not-a-real-secret" not in json.dumps(fleet) + json.dumps(single),
      "secret VALUE leaked into the report")

# Single-cluster scan of c2: legacy-cert IS a finding without fleet context.
check(single_code == 3, f"single scan exit = {single_code}, want 3")
check(single["fleetPresence"] == {"applied": False, "source": "none"},
      f"single fleetPresence = {single['fleetPresence']}")
lc = find(single, "likely-manual", "Secret", "legacy-cert")
check(len(lc) == 1, f"single scan: want legacy-cert finding, got {[f['resource'] for f in single['findings']]}")
if lc:
    check(lc[0]["score"]["total"] == 50 and "presence" not in lc[0],
          f"single legacy-cert: score/presence = {lc[0]['score']['total']}")
extra = [f["resource"] for f in single["findings"] if f["resource"]["name"] != "legacy-cert"]
check(not extra, f"single scan of clean c2 has unexpected findings: {extra}")

if failures:
    print("fleet-smoke: FAIL")
    for m in failures:
        print("  -", m)
    sys.exit(1)
print("fleet-smoke: PASS")
print(f"  fleet findings: {[(f['class'], f['resource']['kind'], f['resource']['name']) for f in fleet['findings']]}")
PY

# --- cluster-scoped leg ------------------------------------------------

echo "fleet-smoke: cluster-scoped scan (c2) must not report system objects..."
set +e
./kube-outlier scan --contexts c2 --include-cluster-scoped -o json > "$WORK/cs.json" 2> "$WORK/cs.err"
cs_code=$?
set -e
python3 - "$WORK/cs.json" "$cs_code" "$WORK/cs.err" <<'PY'
import json, sys
rep = json.load(open(sys.argv[1])); code = int(sys.argv[2]); err = open(sys.argv[3]).read()
fail = []
cs = [f for f in rep["findings"] if not f["resource"]["namespace"]]
if code not in (0, 2): fail.append(f"exit {code}")
# Built-ins (default RBAC, APIServices, APF objects, ...) carry no kubectl
# evidence; anything left must be something a kubectl client wrote.
for f in cs:
    r = f["resource"]
    if not f["ownership"]["kubectlWritten"]:
        fail.append(f"system object reported: {r['kind']} {r['name']}")
    if r["name"].startswith("system:"):
        fail.append(f"system:* object reported: {r['kind']} {r['name']}")
if len(cs) > 10: fail.append(f"{len(cs)} cluster-scoped findings on a clean cluster, want a handful")
if "ComponentStatus is deprecated" in err: fail.append("ComponentStatus still listed (deprecation warning on stderr)")
if fail:
    print("fleet-smoke: cluster-scoped leg FAIL")
    for m in fail[:20]: print("  -", m)
    sys.exit(1)
print(f"fleet-smoke: cluster-scoped leg PASS ({len(cs)} kubectl-made objects: {[f['resource']['name'] for f in cs]})")
PY

# --- baseline leg ------------------------------------------------------

echo "fleet-smoke: generating baseline from c1,c2,c3..."
set +e
./kube-outlier baseline generate --contexts c1,c2,c3 --out "$WORK/artifact.json"
gen_code=$?
set -e
{ [ "$gen_code" -eq 0 ] || [ "$gen_code" -eq 2 ]; } || { echo "fleet-smoke: baseline generate exit $gen_code" >&2; exit 1; }

echo "fleet-smoke: scanning c3 against the baseline..."
set +e
./kube-outlier scan --contexts c3 --baseline "$WORK/artifact.json" \
  --config "$WORK/cfg.yaml" --fail-on inventory -o json > "$WORK/b-c3.json"
b3_code=$?
echo "fleet-smoke: scanning c1 against the baseline..."
./kube-outlier scan --contexts c1 --baseline "$WORK/artifact.json" \
  --config "$WORK/cfg.yaml" --fail-on inventory -o json > "$WORK/b-c1.json"
b1_code=$?

# Scope mismatch must be fatal (exit 1); --allow-scope-mismatch proceeds.
./kube-outlier scan --contexts c3 --baseline "$WORK/artifact.json" \
  --config "$WORK/cfg.yaml" --include-cluster-scoped -o json >/dev/null 2>&1
mismatch_code=$?
./kube-outlier scan --contexts c3 --baseline "$WORK/artifact.json" \
  --config "$WORK/cfg.yaml" --include-cluster-scoped --allow-scope-mismatch -o json >/dev/null 2>&1
allow_code=$?

# Approved exception for the missing NetworkPolicy -> finding suppressed.
python3 - "$WORK/artifact.json" "$WORK/artifact-excepted.json" <<'PY'
import json, sys
b = json.load(open(sys.argv[1]))
b["exceptions"] = [{
    "resource": "networking.k8s.io/NetworkPolicy demo-fleet/default-deny",
    "reason": "intentionally absent on c3 (smoke test)",
    "approvedBy": "fleet-smoke",
}]
json.dump(b, open(sys.argv[2], "w"))
PY
./kube-outlier scan --contexts c3 --baseline "$WORK/artifact-excepted.json" \
  --config "$WORK/cfg.yaml" --fail-on inventory -o json > "$WORK/b-c3-exc.json"
exc_code=$?
set -e

python3 - "$WORK/b-c3.json" "$b3_code" "$WORK/b-c1.json" "$b1_code" \
          "$mismatch_code" "$allow_code" "$WORK/b-c3-exc.json" "$exc_code" <<'PY'
import json, sys

c3 = json.load(open(sys.argv[1])); c3_code = int(sys.argv[2])
c1 = json.load(open(sys.argv[3])); c1_code = int(sys.argv[4])
mismatch_code = int(sys.argv[5]); allow_code = int(sys.argv[6])
exc = json.load(open(sys.argv[7])); exc_code = int(sys.argv[8])
failures = []

def check(cond, msg):
    if not cond:
        failures.append(msg)

for rep, who in [(c3, "c3"), (c1, "c1"), (exc, "c3-excepted")]:
    check(rep["fleetPresence"] == {"applied": True, "source": "baseline"},
          f"{who}: fleetPresence = {rep['fleetPresence']}")

# c3 vs baseline: exactly the withheld NetworkPolicy as missing-from-cluster.
check(c3_code == 3, f"c3 scan exit = {c3_code}, want 3")
c3f = [(f["class"], f["resource"]["name"]) for f in c3["findings"]]
check(c3f == [("missing-from-cluster", "default-deny")], f"c3 findings = {c3f}")

# c1 vs baseline: its two one-offs are in the baseline at ratio 1/3 -> fleet-rare.
check(c1_code == 3, f"c1 scan exit = {c1_code}, want 3")
c1f = sorted(f["resource"]["name"] for f in c1["findings"])
check(c1f == ["debug-dump", "temp-allow-all"], f"c1 findings = {c1f}")
for f in c1["findings"]:
    codes = [r["code"] for r in f["score"]["reasons"]]
    check("fleet-rare" in codes, f"c1 {f['resource']['name']}: reasons = {codes}")

check(mismatch_code == 1, f"scope-mismatch exit = {mismatch_code}, want 1")
check(allow_code != 1, f"--allow-scope-mismatch exit = {allow_code}, want not-1")

# Exception suppresses the missing-from finding.
check(exc_code in (0, 2), f"excepted scan exit = {exc_code}, want 0 or 2")
check(exc["findings"] == [], f"excepted findings = {exc['findings']}")
check(exc["summary"].get("suppressedByException") == 1,
      f"suppressedByException = {exc['summary'].get('suppressedByException')}")

if failures:
    print("fleet-smoke: baseline leg FAIL")
    for m in failures:
        print("  -", m)
    sys.exit(1)
print("fleet-smoke: baseline leg PASS")
PY

# --- why leg -----------------------------------------------------------

echo "fleet-smoke: --why for a one-off and for a missing-from-cluster finding..."
set +e
./kube-outlier scan --contexts c1,c2,c3 --config "$WORK/cfg.yaml" \
  --why "ConfigMap demo-fleet/debug-dump" > "$WORK/explain.txt" 2>&1
explain_code=$?
./kube-outlier scan --contexts c1,c2,c3 --config "$WORK/cfg.yaml" \
  --why "networking.k8s.io/NetworkPolicy demo-fleet/default-deny" > "$WORK/explain-missing.txt" 2>&1
explain_missing_from_code=$?
./kube-outlier scan --contexts c1 --why "ConfigMap demo-fleet/no-such-thing" >/dev/null 2>&1
explain_missing_code=$?
set -e

fail() { echo "fleet-smoke: FAIL — $1" >&2; exit 1; }

[ "$explain_code" -le 2 ] || fail "explain exit $explain_code"
grep -q "status: reported" "$WORK/explain.txt" || fail "explain lacks reported status: $(cat "$WORK/explain.txt")"
grep -q "fleet-rare" "$WORK/explain.txt" || fail "explain lacks fleet-rare evidence"
grep -q "kubectl-client-side-apply" "$WORK/explain.txt" || fail "explain lacks manager evidence"
[ "$explain_missing_from_code" -le 2 ] || fail "explain of default-deny exit $explain_missing_from_code"
grep -q "status: reported as missing-from-cluster: missing from c3" "$WORK/explain-missing.txt" ||
  fail "explain of default-deny must name the missing-from-cluster finding: $(cat "$WORK/explain-missing.txt")"
[ "$explain_missing_code" -eq 1 ] || fail "explain of unknown identity exit $explain_missing_code, want 1"

echo "fleet-smoke: why leg PASS"

# --- namespace leg -------------------------------------------------------

echo "fleet-smoke: namespace leg — planting a one-off in $OTHER_NS on c1..."
kubectl --context c1 create namespace "$OTHER_NS" --dry-run=client -o yaml |
  kubectl --context c1 apply -f - >/dev/null
kubectl --context c1 apply -n "$OTHER_NS" -f - >/dev/null <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: stray-cm
data:
  note: "must only appear when $OTHER_NS is in scope"
EOF

echo "fleet-smoke: creating ServiceAccount ns-reader (read access to $NS only)..."
# Helm-managed (server-side, helm field manager + markers) so the RBAC objects
# themselves never become findings in $NS.
HELM_META="labels: {app.kubernetes.io/managed-by: Helm}
  annotations: {meta.helm.sh/release-name: ns-reader, meta.helm.sh/release-namespace: $NS}"
kubectl --context c1 apply --server-side --field-manager=helm -n "$NS" -f - >/dev/null <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ns-reader
  $HELM_META
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ns-reader
  $HELM_META
rules:
  - apiGroups: ["*"]
    resources: ["*"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ns-reader
  $HELM_META
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ns-reader
subjects:
  - kind: ServiceAccount
    name: ns-reader
    namespace: $NS
EOF
server=$(kubectl config view --raw --minify --flatten --context c1 -o jsonpath='{.clusters[0].cluster.server}')
ca=$(kubectl config view --raw --minify --flatten --context c1 -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')
token=$(kubectl --context c1 create token ns-reader -n "$NS")
cat > "$WORK/ns-reader.kubeconfig" <<EOF
apiVersion: v1
kind: Config
clusters:
  - name: c1
    cluster: {server: "$server", certificate-authority-data: "$ca"}
users:
  - name: ns-reader
    user: {token: "$token"}
contexts:
  - name: c1-ns-reader
    context: {cluster: c1, user: ns-reader}
current-context: c1-ns-reader
EOF
NSKC=(--kubeconfig "$WORK/ns-reader.kubeconfig" --contexts c1-ns-reader)

set +e
./kube-outlier scan --contexts c1 -n "$NS" --fail-on inventory -o json > "$WORK/ns-admin.json"
ns_admin_code=$?
./kube-outlier scan --contexts c1 -n "$NS,$OTHER_NS" -o json > "$WORK/ns-both.json"
ns_both_code=$?
KUBE_OUTLIER_SCOPE__NAMESPACES__INCLUDE="$NS" KUBE_OUTLIER_REPORT__FAILON=inventory \
  ./kube-outlier scan --contexts c1 -o json > "$WORK/ns-env.json"
ns_env_code=$?
./kube-outlier scan --contexts c1 --namespace kube-system --why "ConfigMap kube-system/coredns" > "$WORK/ns-explain-in.txt" 2>&1
explain_in_code=$?
./kube-outlier scan --contexts c1 --why "ConfigMap kube-system/coredns" > "$WORK/ns-explain-out.txt" 2>&1
explain_out_code=$?
./kube-outlier scan "${NSKC[@]}" -o json > "$WORK/sa-wide.json" 2> "$WORK/sa-wide.err"
sa_wide_code=$?
./kube-outlier scan "${NSKC[@]}" -n "$NS" --fail-on inventory -o json > "$WORK/sa-ns.json" 2> "$WORK/sa-ns.err"
sa_ns_code=$?
./kube-outlier scan "${NSKC[@]}" -n "$NS,$OTHER_NS" -o json > "$WORK/sa-mixed.json" 2> "$WORK/sa-mixed.err"
sa_mixed_code=$?
./kube-outlier scan --contexts c1,c2,c3 -n "$NS" --config "$WORK/cfg.yaml" --fail-on inventory -o json > "$WORK/ns-fleet.json"
ns_fleet_code=$?
set -e

python3 - "$WORK" "$NS" "$OTHER_NS" \
  "$ns_admin_code" "$ns_both_code" "$ns_env_code" "$explain_in_code" "$explain_out_code" \
  "$sa_wide_code" "$sa_ns_code" "$sa_mixed_code" "$ns_fleet_code" <<'PY'
import json, sys
work, NS, OTHER = sys.argv[1:4]
(admin_code, both_code, env_code, explain_in_code, explain_out_code,
 sa_wide_code, sa_ns_code, sa_mixed_code, fleet_code) = map(int, sys.argv[4:13])
load = lambda n: json.load(open(f"{work}/{n}"))
fail = []
def check(c, m):
    if not c: fail.append(m)
def names(rep):
    return sorted((f["resource"]["namespace"], f["resource"]["name"]) for f in rep["findings"])

expected = [(NS, "debug-dump"), (NS, "legacy-cert"), (NS, "temp-allow-all")]

# 1. Admin, -n demo-fleet: exactly demo-fleet's findings; the other namespace is never listed.
admin = load("ns-admin.json")
check(admin_code == 3, f"admin -n {NS}: exit {admin_code}, want 3")
check(names(admin) == expected, f"admin -n {NS}: findings {names(admin)}, want {expected}")
check(admin["scope"].get("namespaceInclude") == [NS], f"admin scope = {admin['scope']}")
check(admin["skippedGVRs"] == [], f"admin -n {NS}: unexpected skips {admin['skippedGVRs']}")

# 2. Two namespaces: the stray one-off in the second namespace appears too.
both = load("ns-both.json")
check(both_code == 0, f"admin -n both: exit {both_code}, want 0 (no --fail-on)")
check(names(both) == sorted(expected + [(OTHER, "stray-cm")]), f"admin -n both: findings {names(both)}")

# 3. Same scope via env vars (camelCase failOn must be honoured -> exit 3).
env = load("ns-env.json")
check(env_code == 3, f"env-configured scan: exit {env_code}, want 3 (KUBE_OUTLIER_REPORT__FAILON honoured)")
check(names(env) == expected, f"env-configured scan: findings {names(env)}")

# 4. Naming an excluded namespace overrides the built-in exclusion.
explain_in = open(f"{work}/ns-explain-in.txt").read()
explain_out = open(f"{work}/ns-explain-out.txt").read()
check(explain_in_code <= 2 and "not found in scope" not in explain_in and "kube-system/coredns" in explain_in,
      f"explain with -n kube-system: exit {explain_in_code}\n{explain_in}")
check(explain_out_code == 1 and "not found in scope" in explain_out,
      f"explain without -n: exit {explain_out_code}, want 1 'not found in scope'\n{explain_out}")

# 5. Namespace-only RBAC, cluster-wide: forbidden everywhere, nothing found, partial exit.
wide = load("sa-wide.json")
check(sa_wide_code == 2, f"SA cluster-wide: exit {sa_wide_code}, want 2")
check(wide["findings"] == [], f"SA cluster-wide: findings {names(wide)}, want none")
check(wide["skippedGVRs"] and all(g["forbidden"] and "namespace" not in g for g in wide["skippedGVRs"]),
      f"SA cluster-wide: want only cluster-wide forbidden skips, got {wide['skippedGVRs'][:3]}")
wide_err = open(f"{work}/sa-wide.err").read()
check("resource types forbidden cluster-wide, skipped; if you can read only some namespaces, scan them with -n" in wide_err,
      f"SA cluster-wide: stderr must say everything was forbidden and suggest -n:\n{wide_err}")

# 6. Namespace-only RBAC with -n: full results, nothing skipped.
sa = load("sa-ns.json")
check(sa_ns_code == 3, f"SA -n {NS}: exit {sa_ns_code}, want 3")
check(names(sa) == expected, f"SA -n {NS}: findings {names(sa)}, want {expected}")
check(sa["skippedGVRs"] == [], f"SA -n {NS}: unexpected skips {sa['skippedGVRs'][:3]}")

# 7. Namespace-only RBAC, one allowed + one forbidden namespace: per-namespace skips, no false findings.
mixed = load("sa-mixed.json")
mixed_err = open(f"{work}/sa-mixed.err").read()
check(sa_mixed_code == 2, f"SA mixed: exit {sa_mixed_code}, want 2 (partial)")
check(names(mixed) == expected, f"SA mixed: findings {names(mixed)}, want {expected}")
skipped_ns = {g.get("namespace") for g in mixed["skippedGVRs"]}
check(skipped_ns == {OTHER}, f"SA mixed: skipped namespaces {skipped_ns}, want {{{OTHER!r}}}")
check(f"in namespace {OTHER} forbidden, skipped" in mixed_err, "SA mixed: stderr lacks per-namespace skip line")

# 8. Fleet scan restricted to demo-fleet: the fleet presence signal still works.
fl = load("ns-fleet.json")
check(fleet_code == 3, f"fleet -n {NS}: exit {fleet_code}, want 3")
classes = sorted((f["class"], f["resource"]["name"]) for f in fl["findings"])
for want in [("missing-from-cluster", "default-deny"), ("likely-manual", "debug-dump"), ("likely-manual", "temp-allow-all")]:
    check(want in classes, f"fleet -n {NS}: missing {want} in {classes}")
check(all(f["resource"]["namespace"] == NS for f in fl["findings"]), f"fleet -n {NS}: finding outside {NS}")

dumps = "".join(open(f"{work}/{n}").read() for n in
                ["ns-admin.json", "ns-both.json", "ns-env.json", "sa-ns.json", "sa-mixed.json", "ns-fleet.json"])
check("dummy-not-a-real-secret" not in dumps, "secret VALUE leaked into a namespace-leg report")

if fail:
    print("fleet-smoke: namespace leg FAIL")
    for m in fail: print("  -", m)
    sys.exit(1)
print("fleet-smoke: namespace leg PASS")
PY

# --- content leg ---------------------------------------------------------

echo "fleet-smoke: deploying a common Deployment to all clusters, giving c1 a different image..."
for p in "${PROFILES[@]}"; do
  img="nginx:1.27"
  [ "$p" = "c1" ] && img="nginx:1.25"   # c1 differs from the c2/c3 majority
  kubectl --context "$p" apply --server-side --field-manager=helm -n "$NS" -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: shared-web
  namespace: $NS
  labels: {app.kubernetes.io/managed-by: Helm}
  annotations: {meta.helm.sh/release-name: shared-web, meta.helm.sh/release-namespace: $NS}
spec:
  replicas: 0
  selector: {matchLabels: {app: shared-web}}
  template:
    metadata: {labels: {app: shared-web}}
    spec: {containers: [{name: web, image: $img}]}
EOF
done

set +e
./kube-outlier scan --contexts c1,c2,c3 --config "$WORK/cfg.yaml" \
  --compare-content --fail-on content -o json > "$WORK/content.json" 2>/dev/null
content_code=$?
./kube-outlier scan --contexts c1,c2,c3 --config "$WORK/cfg.yaml" --compare-content > "$WORK/content.txt" 2>/dev/null
set -e

python3 - "$WORK/content.json" "$content_code" <<'PY'
import json, sys
rep = json.load(open(sys.argv[1])); code = int(sys.argv[2])
fail = []
def check(c, m):
    if not c: fail.append(m)

check(code == 4, f"content scan exit = {code}, want 4")
differs = [f for f in rep["findings"] if f["class"] == "content-differs"]
web = [f for f in differs if f["resource"]["name"] == "shared-web"]
check(len(web) == 1, f"want exactly one content-differs finding for shared-web, got {[f['resource']['name'] for f in differs]}")
if web:
    d = web[0]["content"]
    check(web[0]["score"]["reasons"] == [], f"content-differs reasons = {web[0]['score']['reasons']}, want []")
    check(web[0]["clusters"] == ["c1"], f"differing clusters = {web[0]['clusters']}, want [c1]")
    ops = d["differing"][0]["ops"]
    imgops = [o for o in ops if o["path"].endswith("/image")]
    check(len(imgops) == 1, f"want one image op, got {ops}")
    if imgops:
        o = imgops[0]
        check(o["majorityValue"] == "nginx:1.27" and o["clusterValue"] == "nginx:1.25",
              f"image op = {o.get('majorityValue')} -> {o.get('clusterValue')}")
    check(web[0]["presence"]["presentCount"] == 3, f"shared-web presence = {web[0]['presence']}")
# No secret plaintext anywhere in a content report.
check("dummy-not-a-real-secret" not in json.dumps(rep), "secret value leaked into content report")

if fail:
    print("fleet-smoke: content leg FAIL")
    for m in fail: print("  -", m)
    sys.exit(1)
print("fleet-smoke: content leg PASS")
PY

grep -q "shared-web" "$WORK/content.txt" || fail "content table missing shared-web"
grep -q "image: nginx:1.27 → nginx:1.25" "$WORK/content.txt" || fail "content detail missing the image diff: $(cat "$WORK/content.txt")"
grep -q "Fields that differ most:" "$WORK/content.txt" || fail "content detail missing the most-differing fields summary"

# A content gate that cannot run (one cluster, no baseline) must fail the
# run, never pass silently.
set +e
./kube-outlier scan --contexts c2 --compare-content --fail-on content -o json > /dev/null 2> "$WORK/gate.err"
gate_code=$?
set -e
[ "$gate_code" -eq 1 ] || fail "single-cluster content gate exit $gate_code, want 1: $(cat "$WORK/gate.err")"
# A content.kinds entry without its group matches nothing: the gate is
# refused, with the spelling it probably meant, rather than passing forever.
set +e
KUBE_OUTLIER_CONTENT__KINDS=Deployment ./kube-outlier scan --contexts c1,c2,c3 --compare-content --fail-on content -o json > /dev/null 2> "$WORK/kinds.err"
kinds_code=$?
set -e
[ "$kinds_code" -eq 1 ] || fail "content gate with a misspelled kind exit $kinds_code, want 1: $(cat "$WORK/kinds.err")"
grep -q 'did you mean "apps/Deployment"' "$WORK/kinds.err" || fail "misspelled content kind lacks a suggestion: $(cat "$WORK/kinds.err")"
echo "fleet-smoke: content gate PASS"

# --- list-only leg ------------------------------------------------------

echo "fleet-smoke: list-only leg — an identity that may list but not get in $NS on c1 and c2..."
LO_KC="$WORK/list-only.kubeconfig"
for p in c1 c2; do
  kubectl --context "$p" apply -n "$NS" -f - >/dev/null <<EOF
apiVersion: v1
kind: ServiceAccount
metadata: {name: list-only}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: list-only}
rules: [{apiGroups: ["*"], resources: ["*"], verbs: ["list"]}]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: list-only}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: list-only}
subjects: [{kind: ServiceAccount, name: list-only, namespace: $NS}]
EOF
  server=$(kubectl config view --raw --minify --flatten --context "$p" -o jsonpath='{.clusters[0].cluster.server}')
  ca=$(kubectl config view --raw --minify --flatten --context "$p" -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')
  token=$(kubectl --context "$p" create token list-only -n "$NS")
  cat > "$WORK/list-only-$p.kubeconfig" <<EOF
apiVersion: v1
kind: Config
clusters: [{name: lo-$p, cluster: {server: "$server", certificate-authority-data: "$ca"}}]
users: [{name: lo-$p, user: {token: "$token"}}]
contexts: [{name: lo-$p, context: {cluster: lo-$p, user: lo-$p}}]
EOF
done
set +e
# No get anywhere: the content gate cannot run, so it is fatal.
./kube-outlier scan --kubeconfig "$WORK/list-only-c1.kubeconfig" --kubeconfig "$WORK/list-only-c2.kubeconfig" \
  --contexts lo-c1,lo-c2 -n "$NS" --compare-content --fail-on content -o json > /dev/null 2> "$WORK/lo-all.err"
lo_all_code=$?
# get on c1 and c3 (admin), not on c2: a partial run naming the unread copies.
./kube-outlier scan --kubeconfig "$HOME/.kube/config" --kubeconfig "$WORK/list-only-c2.kubeconfig" \
  --contexts c1,lo-c2,c3 -n "$NS" --config "$WORK/cfg.yaml" --compare-content -o json > "$WORK/lo-mixed.json" 2> "$WORK/lo-mixed.err"
lo_mixed_code=$?
set -e
[ "$lo_all_code" -eq 1 ] || fail "list-only content gate exit $lo_all_code, want 1: $(cat "$WORK/lo-all.err")"
grep -q "none of the" "$WORK/lo-all.err" || fail "list-only content gate must say nothing could be read: $(cat "$WORK/lo-all.err")"
[ "$lo_mixed_code" -eq 2 ] || fail "partly readable content comparison exit $lo_mixed_code, want 2: $(cat "$WORK/lo-mixed.err")"
grep -q "lo-c2: get apps/Deployment $NS/shared-web forbidden, left out of the content comparison" "$WORK/lo-mixed.err" ||
  fail "stderr lacks the unread copy: $(cat "$WORK/lo-mixed.err")"
python3 - "$WORK/lo-mixed.json" <<'PY' || fail "skippedObjects lacks the unread copy"
import json, sys
rep = json.load(open(sys.argv[1]))
skipped = rep.get("skippedObjects", [])
assert any(o["cluster"] == "lo-c2" and o["resource"]["name"] == "shared-web" and o["forbidden"] for o in skipped), skipped
# The system object every namespace holds is never compared, so never unread.
assert not any(o["resource"]["name"] == "kube-root-ca.crt" for o in skipped), skipped
PY
echo "fleet-smoke: list-only leg PASS"

# --- secret leg ------------------------------------------------------------

echo "fleet-smoke: secret leg — a kubectl-applied Secret in $SECRET_NS on c1..."
kubectl --context c1 create namespace "$SECRET_NS" --dry-run=client -o yaml |
  kubectl --context c1 apply -f - >/dev/null
# kubectl apply copies the whole manifest, value included, into the
# last-applied-configuration annotation, which the metadata API returns.
kubectl --context c1 apply -n "$SECRET_NS" -f - >/dev/null <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: applied-secret
type: Opaque
stringData:
  password: dummy-applied-not-a-real-secret
EOF
set +e
./kube-outlier scan --contexts c1 -n "$SECRET_NS" -o json > "$WORK/secret.json" 2>/dev/null
secret_code=$?
./kube-outlier scan --contexts c1 -n "$SECRET_NS" -vv --why "Secret $SECRET_NS/applied-secret" > "$WORK/secret-why.txt" 2>&1
set -e
[ "$secret_code" -le 2 ] || fail "secret scan exit $secret_code"
if grep -q "dummy-applied-not-a-real-secret" "$WORK/secret.json" "$WORK/secret-why.txt"; then
  fail "Secret VALUE leaked into a report, --why or a log"
fi
python3 - "$WORK/secret.json" <<'PY' || fail "the report lost the last-applied evidence"
import json, sys
rep = json.load(open(sys.argv[1]))
f = [f for f in rep["findings"] if f["resource"]["name"] == "applied-secret"]
assert f and "last-applied" in [r["code"] for r in f[0]["score"]["reasons"]], rep["findings"]
PY
echo "fleet-smoke: secret leg PASS"

# --- discovery leg -------------------------------------------------------

echo "fleet-smoke: discovery leg — an aggregated API on c1 whose service does not exist..."
kubectl --context c1 apply -f - >/dev/null <<EOF
apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: $BROKEN_APISERVICE
spec:
  group: broken.kube-outlier-smoke.test
  version: v1
  groupPriorityMinimum: 100
  versionPriority: 100
  insecureSkipTLSVerify: true
  service: {name: does-not-exist, namespace: $NS, port: 443}
EOF
# The aggregator marks it unavailable within a few seconds.
for _ in $(seq 1 30); do
  kubectl --context c1 get apiservice "$BROKEN_APISERVICE" -o jsonpath='{.status.conditions[0].status}' 2>/dev/null | grep -q False && break
  sleep 1
done
set +e
./kube-outlier scan --contexts c1 -n "$NS" -o json > "$WORK/discovery.json" 2> "$WORK/discovery.err"
discovery_code=$?
set -e
[ "$discovery_code" -eq 2 ] || fail "scan with a broken aggregated API exit $discovery_code, want 2: $(cat "$WORK/discovery.err")"
grep -q "broken.kube-outlier-smoke.test/v1/\* skipped: discovery failed" "$WORK/discovery.err" ||
  fail "stderr lacks the skipped API group: $(cat "$WORK/discovery.err")"
python3 - "$WORK/discovery.json" <<'PY' || fail "skippedGVRs lacks the broken API group"
import json, sys
rep = json.load(open(sys.argv[1]))
assert any(g["gvr"] == "broken.kube-outlier-smoke.test/v1/*" for g in rep["skippedGVRs"]), rep["skippedGVRs"]
PY
kubectl --context c1 delete apiservice "$BROKEN_APISERVICE" --ignore-not-found >/dev/null
echo "fleet-smoke: discovery leg PASS"

echo "fleet-smoke: ALL PASS"
