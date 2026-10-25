package normalize_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/normalize"
)

func deployKey() identity.ResourceKey {
	return identity.ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "api"}
}

func deployBody(image, cpu string, extraEnv []interface{}, mut func(map[string]interface{})) map[string]interface{} {
	container := map[string]interface{}{
		"name":                     "api",
		"image":                    image,
		"terminationMessagePath":   "/dev/termination-log",
		"terminationMessagePolicy": "File",
		"imagePullPolicy":          "IfNotPresent",
		"resources": map[string]interface{}{
			"limits":   map[string]interface{}{"cpu": cpu, "memory": "1Gi"},
			"requests": map[string]interface{}{"cpu": cpu},
		},
	}
	if extraEnv != nil {
		container["env"] = extraEnv
	}
	body := map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": "api", "namespace": "shop"},
		"spec": map[string]interface{}{
			"replicas": float64(3),
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"dnsPolicy":     "ClusterFirst",
					"schedulerName": "default-scheduler",
					"restartPolicy": "Always",
					"containers":    []interface{}{container},
				},
			},
		},
	}
	if mut != nil {
		mut(body)
	}
	return body
}

func hashOf(t *testing.T, body map[string]interface{}, extraIgnore []string) string {
	t.Helper()
	canon := normalize.CanonicalizeForComparison(body, deployKey(), extraIgnore, false)
	h, err := normalize.CanonicalHash(canon)
	if err != nil {
		t.Fatalf("CanonicalHash: %v", err)
	}
	return h
}

// Aggressive server-default strips: two copies differing only in stripped
// fields hash equal.
func TestCanonicalize_StripsServerDefaults(t *testing.T) {
	a := deployBody("api:v1", "100m", nil, nil)
	b := deployBody("api:v1", "100m", nil, func(body map[string]interface{}) {
		ts := body["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
		ts["schedulerName"] = "custom-scheduler" // stripped
		ts["dnsPolicy"] = "Default"              // stripped
		c := ts["containers"].([]interface{})[0].(map[string]interface{})
		c["imagePullPolicy"] = "Always" // stripped
	})
	if hashOf(t, a, nil) != hashOf(t, b, nil) {
		t.Error("copies differing only in stripped server defaults must hash equal")
	}
}

// A real image difference is NOT stripped → different hash.
func TestCanonicalize_ImageSkewDiffers(t *testing.T) {
	if hashOf(t, deployBody("api:v1", "100m", nil, nil), nil) ==
		hashOf(t, deployBody("api:v2", "100m", nil, nil), nil) {
		t.Error("image skew must change the hash")
	}
}

// Quantity canonicalization: 100m == 0.1, 1Gi == 1024Mi.
func TestCanonicalize_Quantities(t *testing.T) {
	if hashOf(t, deployBody("api:v1", "100m", nil, nil), nil) !=
		hashOf(t, deployBody("api:v1", "0.1", nil, nil), nil) {
		t.Error("100m and 0.1 must canonicalize equal")
	}
	memGi := deployBody("api:v1", "100m", nil, nil)
	memMi := deployBody("api:v1", "100m", nil, func(body map[string]interface{}) {
		c := body["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})["containers"].([]interface{})[0].(map[string]interface{})
		c["resources"].(map[string]interface{})["limits"].(map[string]interface{})["memory"] = "1024Mi"
	})
	if hashOf(t, memGi, nil) != hashOf(t, memMi, nil) {
		t.Error("1Gi and 1024Mi must canonicalize equal")
	}
}

// Env var ORDER is significant (references resolve top-to-bottom).
func TestCanonicalize_EnvOrderMatters(t *testing.T) {
	e1 := []interface{}{
		map[string]interface{}{"name": "A", "value": "1"},
		map[string]interface{}{"name": "B", "value": "$(A)"},
	}
	e2 := []interface{}{
		map[string]interface{}{"name": "B", "value": "$(A)"},
		map[string]interface{}{"name": "A", "value": "1"},
	}
	if hashOf(t, deployBody("api:v1", "100m", e1, nil), nil) ==
		hashOf(t, deployBody("api:v1", "100m", e2, nil), nil) {
		t.Error("reordered env must change the hash (order carries meaning)")
	}
}

// Empty map/slice ⇔ absent.
func TestCanonicalize_EmptyEqualsNil(t *testing.T) {
	withEmpty := deployBody("api:v1", "100m", []interface{}{}, func(body map[string]interface{}) {
		body["metadata"].(map[string]interface{})["labels"] = map[string]interface{}{}
	})
	without := deployBody("api:v1", "100m", nil, nil)
	if hashOf(t, withEmpty, nil) != hashOf(t, without, nil) {
		t.Error("empty env:[] / labels:{} must equal their absence")
	}
}

// User ignoreFields with a bracket-quoted annotation key.
func TestCanonicalize_UserIgnoreFields(t *testing.T) {
	base := deployBody("api:v1", "100m", nil, nil)
	tagged := deployBody("api:v1", "100m", nil, func(body map[string]interface{}) {
		body["metadata"].(map[string]interface{})["annotations"] = map[string]interface{}{
			"team.example.com/deployed-at": "2026-09-18",
		}
	})
	ignore := []string{`metadata.annotations["team.example.com/deployed-at"]`}
	if hashOf(t, base, ignore) != hashOf(t, tagged, ignore) {
		t.Error("ignored annotation must not affect the hash")
	}
	// ...but without ignoring it, it does.
	if hashOf(t, base, nil) == hashOf(t, tagged, nil) {
		t.Error("un-ignored annotation must affect the hash")
	}
}

// The paths a report prints ("containers[0].image") and the JSON pointers
// the JSON report uses work as ignore paths too, like the "[*]" form.
func TestCanonicalize_IgnoreFieldsPathForms(t *testing.T) {
	v1 := deployBody("api:v1", "100m", nil, nil)
	v2 := deployBody("api:v2", "100m", nil, nil)
	for _, path := range []string{
		"spec.template.spec.containers[*].image",
		"spec.template.spec.containers[0].image",
		"/spec/template/spec/containers/0/image",
	} {
		if hashOf(t, v1, []string{path}) != hashOf(t, v2, []string{path}) {
			t.Errorf("%s: ignored image must not affect the hash", path)
		}
	}
	// An index past the end, or on a non-array, matches nothing.
	for _, path := range []string{"spec.template.spec.containers[1].image", "spec.replicas[0]"} {
		if hashOf(t, v1, []string{path}) == hashOf(t, v2, []string{path}) {
			t.Errorf("%s must not match the image", path)
		}
	}
	// A terminal index drops that element.
	two := deployBody("api:v1", "100m", nil, func(body map[string]interface{}) {
		spec := body["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
		spec["containers"] = append(spec["containers"].([]interface{}), map[string]interface{}{"name": "sidecar", "image": "proxy:1"})
	})
	if hashOf(t, two, []string{"spec.template.spec.containers[1]"}) != hashOf(t, v1, nil) {
		t.Error("containers[1] must drop the second container")
	}
}

func TestPathMatches(t *testing.T) {
	body := deployBody("api:v1", "100m", nil, nil)
	for path, want := range map[string]bool{
		"spec.replicas":                             true,
		"spec.template.spec.containers[*].image":    true,
		"spec.template.spec.containers[0].image":    true,
		"/spec/template/spec/containers/0/image":    true,
		"spec.templte.spec":                         false,
		"spec.template.spec.containers[3].image":    false,
		"metadata.annotations[\"x.example.com/y\"]": false,
	} {
		if got := normalize.PathMatches(body, path); got != want {
			t.Errorf("PathMatches(%q) = %v, want %v", path, got, want)
		}
	}
}

// Secret data is hashed to sha256: — never plaintext.
func TestCanonicalize_SecretHashing(t *testing.T) {
	key := identity.ResourceKey{Kind: "Secret", Namespace: "shop", Name: "cert"}
	secret := map[string]interface{}{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]interface{}{"name": "cert", "namespace": "shop"},
		"data":     map[string]interface{}{"tls.key": "c3VwZXItc2VjcmV0LXZhbHVl"},
	}
	canon := normalize.CanonicalizeForComparison(secret, key, nil, false)
	raw, _ := normalize.CanonicalJSON(canon)
	if strings.Contains(string(raw), "c3VwZXItc2VjcmV0LXZhbHVl") {
		t.Errorf("plaintext secret survived into canonical JSON:\n%s", raw)
	}
	if !strings.Contains(string(raw), "sha256:") {
		t.Errorf("secret value not hashed:\n%s", raw)
	}

	// Identical secrets hash equal; different ones differ.
	h1, _ := normalize.CanonicalHash(canon)
	same := normalize.CanonicalizeForComparison(secret, key, nil, false)
	h2, _ := normalize.CanonicalHash(same)
	if h1 != h2 {
		t.Error("identical secrets must hash equal")
	}
	secret2 := map[string]interface{}{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]interface{}{"name": "cert", "namespace": "shop"},
		"data":     map[string]interface{}{"tls.key": "ZGlmZmVyZW50"},
	}
	h3, _ := normalize.CanonicalHash(normalize.CanonicalizeForComparison(secret2, key, nil, false))
	if h1 == h3 {
		t.Error("different secrets must hash differently")
	}
}

// HashSecretData is pure and reports the flag; non-Secret untouched.
func TestHashSecretData(t *testing.T) {
	secret := map[string]interface{}{
		"kind": "Secret",
		"data": map[string]interface{}{"k": "dg=="},
	}
	got, hashed := normalize.HashSecretData(secret)
	if !hashed || !strings.HasPrefix(got["data"].(map[string]interface{})["k"].(string), "sha256:") {
		t.Errorf("want hashed value, got %v", got)
	}
	if secret["data"].(map[string]interface{})["k"] != "dg==" {
		t.Error("input must not be mutated")
	}
	if _, hashed := normalize.HashSecretData(map[string]interface{}{"kind": "ConfigMap"}); hashed {
		t.Error("non-Secret must report hashed=false")
	}
}

// CanonicalJSON sorts keys (stable hash regardless of insertion order).
func TestCanonicalJSON_KeyOrderStable(t *testing.T) {
	a := map[string]interface{}{"b": 1, "a": 2, "c": 3}
	b := map[string]interface{}{"c": 3, "a": 2, "b": 1}
	ja, _ := normalize.CanonicalJSON(a)
	jb, _ := normalize.CanonicalJSON(b)
	if string(ja) != string(jb) {
		t.Errorf("canonical JSON must be key-order-independent: %s vs %s", ja, jb)
	}
	if !json.Valid(ja) {
		t.Error("not valid JSON")
	}
}

// fakeGenerator is a custom resource that holds credentials in its spec, like
// the external-secrets Fake generator.
func fakeGenerator(password string) map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": "generators.external-secrets.io/v1alpha1",
		"kind":       "Fake",
		"metadata":   map[string]interface{}{"name": "db", "namespace": "shop"},
		"spec": map[string]interface{}{
			"data":  map[string]interface{}{"password": password},
			"ports": []interface{}{int64(5432), true},
		},
	}
}

var fakeKey = identity.ResourceKey{Group: "generators.external-secrets.io", Kind: "Fake", Namespace: "shop", Name: "db"}

// A sensitive.kinds kind has every value hashed for comparison: no plaintext
// survives, equal values still hash equal, and the field paths stay.
func TestCanonicalizeForComparison_SensitiveKind(t *testing.T) {
	canon := normalize.CanonicalizeForComparison(fakeGenerator("hunter2"), fakeKey, nil, true)
	raw, _ := json.Marshal(canon)
	if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "5432") {
		t.Fatalf("plaintext value survived: %s", raw)
	}
	pw, _ := canon["spec"].(map[string]interface{})["data"].(map[string]interface{})["password"].(string)
	if !strings.HasPrefix(pw, "sha256:") {
		t.Errorf("password = %q, want a sha256: digest at the same path", pw)
	}
	if canon["metadata"].(map[string]interface{})["name"] != "db" {
		t.Errorf("metadata must stay readable, got %v", canon["metadata"])
	}

	same, _ := normalize.CanonicalHash(normalize.CanonicalizeForComparison(fakeGenerator("hunter2"), fakeKey, nil, true))
	other, _ := normalize.CanonicalHash(normalize.CanonicalizeForComparison(fakeGenerator("hunter3"), fakeKey, nil, true))
	first, _ := normalize.CanonicalHash(canon)
	if first != same || first == other {
		t.Errorf("hashes: equal values must match and different ones differ (%s %s %s)", first, same, other)
	}

	plain := normalize.CanonicalizeForComparison(fakeGenerator("hunter2"), fakeKey, nil, false)
	if raw, _ := json.Marshal(plain); !strings.Contains(string(raw), "hunter2") {
		t.Errorf("a kind not in sensitive.kinds is compared as-is, got %s", raw)
	}
}
