package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/connect"
	"github.com/vibe-shift-coding/kube-outlier/internal/discovery"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

// configMapCluster is one scanned cluster holding the ConfigMaps names in
// namespace "app", each with data {"value": value}, served through a fake
// dynamic client; getErr, when set, is what every get of a ConfigMap returns.
// gets counts the gets the client received.
func configMapCluster(t *testing.T, cluster, value string, getErr error, gets *int, names ...string) clusterResult {
	t.Helper()
	var objs []runtime.Object
	var resources []inventory.Resource
	for _, name := range names {
		objs = append(objs, &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]interface{}{"name": name, "namespace": "app"},
			"data":       map[string]interface{}{"value": value},
		}})
		resources = append(resources, inventory.Resource{
			Key:               identity.ResourceKey{Kind: "ConfigMap", Namespace: "app", Name: name},
			Cluster:           cluster,
			ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "helm"}},
			CreationTimestamp: metav1.NewTime(testNow),
		})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{cmGVR: "ConfigMapList"}, objs...)
	dyn.PrependReactor("get", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if gets != nil {
			*gets++
		}
		if getErr != nil {
			return true, nil, getErr
		}
		return false, nil, nil
	})
	return clusterResult{
		snap:    &inventory.Snapshot{Cluster: cluster, Resources: resources},
		gvrs:    []discovery.GVRInfo{{GVR: cmGVR, Kind: "ConfigMap", Namespaced: true}},
		clients: &connect.Clients{Dynamic: dyn},
	}
}

func forbidden(name string) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, name, errors.New("no get"))
}

// System objects are never content-compared. kube-root-ca.crt holds each
// cluster's own CA, so on a real fleet every namespace would otherwise yield
// a content-differs finding, and the object is never even fetched.
func TestContentComparison_SkipsSystemObjects(t *testing.T) {
	cfg := config.Default()
	gets := 0
	results := []clusterResult{
		configMapCluster(t, "a", "ca-of-a", nil, &gets, "kube-root-ca.crt"),
		configMapCluster(t, "b", "ca-of-b", nil, &gets, "kube-root-ca.crt"),
		configMapCluster(t, "c", "ca-of-c", nil, &gets, "kube-root-ca.crt"),
	}

	res, err := runContentComparison(t.Context(), results, &cfg, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.findings) != 0 {
		t.Errorf("want no finding for kube-root-ca.crt, got %+v", res.findings)
	}
	if gets != 0 {
		t.Errorf("system objects must not be fetched, got %d gets", gets)
	}
	if res.skipReason != "" {
		t.Errorf("nothing to compare is not a skipped comparison, got %q", res.skipReason)
	}
}

// A copy that cannot be read is reported, never silently left out: one
// forbidden cluster makes the run partial, and a comparison that could read
// nothing cannot back a content gate.
func TestContentComparison_UnreadCopiesAreReported(t *testing.T) {
	cfg := config.Default()

	// One cluster forbids get: its copy is listed as unread, the other two
	// are still compared.
	results := []clusterResult{
		configMapCluster(t, "a", "v1", nil, nil, "settings"),
		configMapCluster(t, "b", "v1", forbidden("settings"), nil, "settings"),
		configMapCluster(t, "c", "v2", nil, nil, "settings"),
	}
	res, err := runContentComparison(t.Context(), results, &cfg, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.unread) != 1 || res.unread[0].Cluster != "b" || !res.unread[0].Forbidden || res.unread[0].Resource.Name != "settings" {
		t.Errorf("unread = %+v, want settings on b, forbidden", res.unread)
	}
	if res.skipReason != "" {
		t.Errorf("a comparison that read some copies ran; skipReason = %q", res.skipReason)
	}

	// Every get forbidden (list-only RBAC): the comparison could not run.
	results = []clusterResult{
		configMapCluster(t, "a", "v1", forbidden("settings"), nil, "settings"),
		configMapCluster(t, "b", "v1", forbidden("settings"), nil, "settings"),
	}
	res, err = runContentComparison(t.Context(), results, &cfg, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.unread) != 2 || !strings.Contains(res.skipReason, "none of the 2 copies") {
		t.Errorf("all forbidden: unread=%+v skipReason=%q", res.unread, res.skipReason)
	}

	// An object deleted since it was listed is gone, not unread.
	results = []clusterResult{
		configMapCluster(t, "a", "v1", nil, nil, "settings"),
		configMapCluster(t, "b", "v1", apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "settings"), nil, "settings"),
	}
	res, err = runContentComparison(t.Context(), results, &cfg, testNow)
	if err != nil || len(res.unread) != 0 {
		t.Errorf("deleted copy: unread=%+v err=%v, want none", res.unread, err)
	}
}

// End to end: the exit code reflects copies the comparison could not read.
func TestScan_ContentGateWithUnreadableCopies(t *testing.T) {
	// No get anywhere: a content gate is fatal rather than passing.
	fakeFleet(t,
		configMapCluster(t, "a", "v1", forbidden("settings"), nil, "settings"),
		configMapCluster(t, "b", "v2", forbidden("settings"), nil, "settings"),
	)
	code, _, stderr := runCLI(t, "scan", "--compare-content", "--fail-on", "content")
	if code != 1 || !strings.Contains(stderr, "none of the 2 copies") {
		t.Errorf("all copies forbidden: exit=%d stderr=%q, want exit 1", code, stderr)
	}

	// One cluster without get: a partial run (exit 2), with the copy in the
	// JSON; --allow-partial turns it into 0.
	fakeFleet(t,
		configMapCluster(t, "a", "v1", nil, nil, "settings"),
		configMapCluster(t, "b", "v1", nil, nil, "settings"),
		configMapCluster(t, "c", "v1", forbidden("settings"), nil, "settings"),
	)
	code, stdout, stderr := runCLI(t, "scan", "--compare-content", "--fail-on", "content", "-o", "json")
	if code != 2 {
		t.Fatalf("one copy forbidden: exit=%d stderr=%q, want 2 (partial)", code, stderr)
	}
	var rep struct {
		SkippedObjects []struct {
			Cluster   string               `json:"cluster"`
			Resource  identity.ResourceKey `json:"resource"`
			Forbidden bool                 `json:"forbidden"`
		} `json:"skippedObjects"`
	}
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.SkippedObjects) != 1 || rep.SkippedObjects[0].Cluster != "c" || !rep.SkippedObjects[0].Forbidden {
		t.Errorf("skippedObjects = %+v, want the copy on c", rep.SkippedObjects)
	}
	if code, _, stderr := runCLI(t, "scan", "--compare-content", "--allow-partial"); code != 0 {
		t.Errorf("--allow-partial: exit=%d stderr=%q, want 0", code, stderr)
	}
	if code, _, _ := runCLI(t, "scan", "--compare-content", "--why", "ConfigMap app/settings"); code != 2 {
		t.Errorf("--why with an unread copy: exit=%d, want 2 (partial)", code)
	}
}
