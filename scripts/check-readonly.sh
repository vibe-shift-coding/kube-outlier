#!/usr/bin/env bash
# Enforces the read-only invariant (docs/DESIGN.md, docs/interfaces.md §1): no code
# outside tests may call — or take a method value of — a mutating Kubernetes client
# method, or build a raw REST request with a write verb. The runtime half of the
# rule is connect's transport, which refuses any method other than GET and HEAD.
set -euo pipefail

verbs='Create|Update|UpdateStatus|UpdateScale|Patch|Delete|DeleteCollection|Apply|ApplyStatus|ApplyScale|Evict|EvictV1|EvictV1beta1'
# A call (".Delete(") or a method value (".Delete" followed by anything that
# continues no identifier, e.g. "del := ri.Delete").
calls="\\.(${verbs})(\\(|[^A-Za-z0-9_(]|\$)"
# Raw REST: RESTClient().Post()/Put()/Patch()/Delete() or .Verb("POST").
rest='RESTClient\(|\.(Post|Put)\(|\.Verb\('

matches=$(grep -rnE "$calls|$rest" cmd/ internal/ --include='*.go' --exclude='*_test.go' || true)

if [ -n "$matches" ]; then
	echo "check-readonly: found mutating client call(s):" >&2
	echo "$matches" >&2
	exit 1
fi

echo "check-readonly: OK, no mutating client calls found."
