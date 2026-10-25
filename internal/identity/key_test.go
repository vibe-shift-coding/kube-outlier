package identity

import (
	"encoding/json"
	"testing"
)

func TestResourceKeyString(t *testing.T) {
	cases := []struct {
		name string
		key  ResourceKey
		want string
	}{
		{
			name: "core group namespaced",
			key:  ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "x"},
			want: "ConfigMap default/x",
		},
		{
			name: "grouped namespaced",
			key:  ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "api"},
			want: "apps/Deployment shop/api",
		},
		{
			name: "grouped cluster-scoped",
			key:  ResourceKey{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "admin"},
			want: "rbac.authorization.k8s.io/ClusterRole admin",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.key.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// JSON carries the identity string as "id" after the four fields, and
// reading ignores it.
func TestResourceKeyJSON(t *testing.T) {
	k := ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "api"}
	raw, err := json.Marshal(k)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"group":"apps","kind":"Deployment","namespace":"shop","name":"api","id":"apps/Deployment shop/api"}`
	if string(raw) != want {
		t.Errorf("Marshal = %s, want %s", raw, want)
	}
	var back ResourceKey
	if err := json.Unmarshal(raw, &back); err != nil || back != k {
		t.Errorf("Unmarshal = %+v (%v), want %+v", back, err, k)
	}
}

func TestPresenceMatrix(t *testing.T) {
	m := NewPresenceMatrix([]string{"b", "a", "c"})
	if len(m.Clusters) != 3 || m.Clusters[0] != "a" || m.Clusters[1] != "b" || m.Clusters[2] != "c" {
		t.Fatalf("Clusters not sorted: %v", m.Clusters)
	}

	k := ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "x"}
	m.Add(k, "a")
	m.Add(k, "c")

	presentOn := m.PresentOn(k)
	if len(presentOn) != 2 || presentOn[0] != "a" || presentOn[1] != "c" {
		t.Errorf("PresentOn = %v, want [a c]", presentOn)
	}

	missingFrom := m.MissingFrom(k)
	if len(missingFrom) != 1 || missingFrom[0] != "b" {
		t.Errorf("MissingFrom = %v, want [b]", missingFrom)
	}

	if got := m.Ratio(k); got != 2.0/3.0 {
		t.Errorf("Ratio = %v, want %v", got, 2.0/3.0)
	}

	unknown := ResourceKey{Kind: "Secret", Namespace: "default", Name: "unknown"}
	if got := m.Ratio(unknown); got != 0 {
		t.Errorf("Ratio(unknown) = %v, want 0", got)
	}
	if got := m.MissingFrom(unknown); len(got) != 3 {
		t.Errorf("MissingFrom(unknown) = %v, want all 3 clusters", got)
	}
}
