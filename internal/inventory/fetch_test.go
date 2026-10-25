package inventory_test

import (
	"context"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

var (
	cmGVR  = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	nsGVR  = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	scheme = func() *runtime.Scheme {
		s := runtime.NewScheme()
		s.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMapList"}, &unstructured.UnstructuredList{})
		s.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "NamespaceList"}, &unstructured.UnstructuredList{})
		return s
	}()
)

func TestFetchBody(t *testing.T) {
	cm := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "debug-dump", "namespace": "default"},
		"data":       map[string]interface{}{"note": "hi"},
	}}
	ns := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]interface{}{"name": "shop"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, cm, ns)

	body, err := inventory.FetchBody(context.Background(), dyn, cmGVR,
		identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "debug-dump"})
	if err != nil {
		t.Fatalf("namespaced FetchBody: %v", err)
	}
	if !reflect.DeepEqual(body["data"], map[string]interface{}{"note": "hi"}) {
		t.Errorf("body = %v", body)
	}

	body, err = inventory.FetchBody(context.Background(), dyn, nsGVR,
		identity.ResourceKey{Kind: "Namespace", Name: "shop"})
	if err != nil {
		t.Fatalf("cluster-scoped FetchBody: %v", err)
	}
	if body["kind"] != "Namespace" {
		t.Errorf("body = %v", body)
	}

	_, err = inventory.FetchBody(context.Background(), dyn, cmGVR,
		identity.ResourceKey{Kind: "ConfigMap", Namespace: "default", Name: "vanished"})
	if err == nil || !apierrors.IsNotFound(err) {
		t.Errorf("want IsNotFound-compatible error, got %v", err)
	}
}
