package normalize_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/normalize"
)

func loadBody(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("../../testdata/normalize", name))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	if err := yaml.Unmarshal(raw, &body); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return body
}

func TestNormalize_Fixtures(t *testing.T) {
	tests := []struct {
		input, want string
		key         identity.ResourceKey
	}{
		{"service.yaml", "service.want.yaml",
			identity.ResourceKey{Kind: "Service", Namespace: "shop", Name: "web"}},
		{"deployment.yaml", "deployment.want.yaml",
			identity.ResourceKey{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "api"}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalize.Normalize(loadBody(t, tt.input), tt.key)
			want := loadBody(t, tt.want)
			if !reflect.DeepEqual(got, want) {
				gotY, _ := yaml.Marshal(got)
				wantY, _ := yaml.Marshal(want)
				t.Errorf("normalize mismatch:\n--- got ---\n%s\n--- want ---\n%s", gotY, wantY)
			}
		})
	}
}

// Normalize must never mutate its input (pure function contract).
func TestNormalize_InputUntouched(t *testing.T) {
	body := loadBody(t, "service.yaml")
	before, _ := yaml.Marshal(body)
	normalize.Normalize(body, identity.ResourceKey{Kind: "Service", Namespace: "shop", Name: "web"})
	after, _ := yaml.Marshal(body)
	if string(before) != string(after) {
		t.Error("Normalize mutated its input")
	}
}

// Nonexistent paths and unknown kinds are no-ops beyond the global drops.
func TestNormalize_UnknownKindAndMissingPaths(t *testing.T) {
	body := map[string]interface{}{
		"apiVersion": "acme.example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]interface{}{"name": "w", "resourceVersion": "5"},
		"spec":       map[string]interface{}{"size": "large"},
	}
	got := normalize.Normalize(body, identity.ResourceKey{Group: "acme.example.com", Kind: "Widget", Name: "w"})
	meta := got["metadata"].(map[string]interface{})
	if _, ok := meta["resourceVersion"]; ok {
		t.Error("global drop must still apply to unknown kinds")
	}
	if !reflect.DeepEqual(got["spec"], body["spec"]) {
		t.Errorf("spec must be untouched, got %v", got["spec"])
	}
}
