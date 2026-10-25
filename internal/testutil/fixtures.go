// Package testutil provides shared test helpers for loading the Tier 1 fixture
// manifests (testdata/fixtures/) into inventory.Resource values.
package testutil

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

// manifest is the subset of a Kubernetes manifest LoadFixtures needs: GVK
// plus full ObjectMeta (labels, annotations, ownerReferences, managedFields,
// creationTimestamp, uid).
type manifest struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   metav1.ObjectMeta `json:"metadata"`
}

// LoadFixtures parses every *.yaml file in dir into an inventory.Resource
// (Cluster: "fixture"), sorted by file name for determinism.
func LoadFixtures(t *testing.T, dir string) []inventory.Resource {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading fixtures dir %s: %v", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	out := make([]inventory.Resource, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading fixture %s: %v", name, err)
		}
		out = append(out, parseFixture(t, name, data))
	}
	return out
}

func parseFixture(t *testing.T, name string, data []byte) inventory.Resource {
	t.Helper()

	var m manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parsing fixture %s: %v", name, err)
	}

	gv, err := schema.ParseGroupVersion(m.APIVersion)
	if err != nil {
		t.Fatalf("fixture %s: parsing apiVersion %q: %v", name, m.APIVersion, err)
	}

	return inventory.Resource{
		Key: identity.ResourceKey{
			Group:     gv.Group,
			Kind:      m.Kind,
			Namespace: m.Metadata.Namespace,
			Name:      m.Metadata.Name,
		},
		Cluster:           "fixture",
		APIVersion:        m.APIVersion,
		UID:               string(m.Metadata.UID),
		Labels:            m.Metadata.Labels,
		Annotations:       m.Metadata.Annotations,
		OwnerReferences:   m.Metadata.OwnerReferences,
		ManagedFields:     m.Metadata.ManagedFields,
		CreationTimestamp: m.Metadata.CreationTimestamp,
	}
}
