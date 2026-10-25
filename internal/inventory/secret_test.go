package inventory_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/discovery"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

// sentinelValue stands in for a Secret value. It must never survive Pass A.
const sentinelValue = "SENTINEL-NOT-A-REAL-SECRET"

// A Secret created with `kubectl apply` carries its whole manifest, values
// included, in the last-applied-configuration annotation. Pass A must keep
// the annotation's presence (it is ownership evidence) and drop its value, so
// nothing built from the snapshot can contain the Secret's data.
func TestCollectMetadata_LastAppliedValueNeverKept(t *testing.T) {
	secretGVRInfo := discovery.GVRInfo{GVR: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, Kind: "Secret", Namespaced: true}
	const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"
	obj := pom("payments", "db", "uid-1")
	obj.Annotations = map[string]string{
		lastApplied: `{"apiVersion":"v1","kind":"Secret","stringData":{"password":"` + sentinelValue + `"}}`,
		"team":      "payments",
	}
	client := newFakeMetadataClient()
	client.PrependReactor("list", "secrets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, listOf("", obj), nil
	})

	snap, err := inventory.CollectMetadata(context.Background(), "c1", client, []discovery.GVRInfo{secretGVRInfo}, config.ScopeConfig{}, inventory.Options{})
	if err != nil {
		t.Fatalf("CollectMetadata: %v", err)
	}
	ann := snap.Resources[0].Annotations
	if v, ok := ann[lastApplied]; !ok || v != "" {
		t.Fatalf("want last-applied key kept with an empty value, got present=%v value=%q", ok, v)
	}
	if ann["team"] != "payments" {
		t.Errorf("other annotations must be kept, got %v", ann)
	}
	if !strings.Contains(obj.Annotations[lastApplied], sentinelValue) {
		t.Error("the listed object must not be mutated")
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sentinelValue) {
		t.Fatal("the snapshot contains a Secret value")
	}
}
