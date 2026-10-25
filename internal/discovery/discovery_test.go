package discovery_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sdiscovery "k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/discovery"
)

func newFake(resources ...*metav1.APIResourceList) *fakediscovery.FakeDiscovery {
	return &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{Resources: resources}}
}

func listVerbs(verbs ...string) metav1.Verbs { return metav1.Verbs(verbs) }

func TestListScannable_VersionAgnostic(t *testing.T) {
	// Two versions of the same group/kind; only the preferred (first-seen)
	// version's resource should survive, and identity should never carry
	// apiVersion.
	fake := newFake(&metav1.APIResourceList{
		GroupVersion: "apps/v1",
		APIResources: []metav1.APIResource{
			{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: listVerbs("list", "get")},
		},
	})

	res, err := discovery.ListScannable(context.Background(), fake, config.ScopeConfig{})
	got := res.GVRs
	if err != nil {
		t.Fatalf("ListScannable: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 GVR, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "Deployment" || got[0].GVR.Group != "apps" {
		t.Fatalf("unexpected result: %+v", got[0])
	}
}

func TestListScannable_PartialDiscoveryFailureProceeds(t *testing.T) {
	inner := newFake(
		&metav1.APIResourceList{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: listVerbs("list", "get")},
			},
		},
		&metav1.APIResourceList{
			GroupVersion: "batch/v1",
			APIResources: []metav1.APIResource{
				{Name: "jobs", Kind: "Job", Namespaced: true, Verbs: listVerbs("list", "get")},
			},
		},
	)
	fake := &erroringDiscovery{FakeDiscovery: inner, failGV: "batch/v1", failErr: errors.New("aggregated api down")}

	res, err := discovery.ListScannable(context.Background(), fake, config.ScopeConfig{})
	got, failed := res.GVRs, res.Failed
	if err != nil {
		t.Fatalf("ListScannable should proceed on partial discovery failure, got error: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "Deployment" {
		t.Fatalf("want only Deployment surviving the partial failure, got %+v", got)
	}
	// The failed group is returned so the run can report it as skipped.
	if len(failed) != 1 || failed[0].GroupVersion.String() != "batch/v1" || !strings.Contains(failed[0].Err, "aggregated api down") {
		t.Errorf("failed = %+v, want batch/v1 with its error", failed)
	}
}

// The aggregated metrics APIs are often down and their types are never
// scanned, so their failure is not reported.
func TestListScannable_OptionalGroupFailureIgnored(t *testing.T) {
	inner := newFake(
		&metav1.APIResourceList{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: listVerbs("list", "get")},
			},
		},
		&metav1.APIResourceList{GroupVersion: "metrics.k8s.io/v1beta1"},
	)
	fake := &erroringDiscovery{FakeDiscovery: inner, failGV: "metrics.k8s.io/v1beta1", failErr: errors.New("metrics-server down")}

	res, err := discovery.ListScannable(context.Background(), fake, config.ScopeConfig{})
	got, failed := res.GVRs, res.Failed
	if err != nil || len(got) != 1 {
		t.Fatalf("ListScannable = %+v, %v", got, err)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %+v, want the metrics API ignored", failed)
	}
}

func TestListScannable_OtherDiscoveryErrorReturned(t *testing.T) {
	fake := newFake()
	fake.PrependReactor("get", "group", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("server unavailable")
	})

	_, err := discovery.ListScannable(context.Background(), fake, config.ScopeConfig{})
	if err == nil {
		t.Fatal("want error for non-group-discovery-failure, got nil")
	}
}

func TestListScannable_RequiresListAndGetVerbs(t *testing.T) {
	fake := newFake(&metav1.APIResourceList{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: listVerbs("list", "get")},
			{Name: "watchonly", Kind: "WatchOnly", Namespaced: true, Verbs: listVerbs("list", "watch")},
		},
	})

	res, err := discovery.ListScannable(context.Background(), fake, config.ScopeConfig{})
	got := res.GVRs
	if err != nil {
		t.Fatalf("ListScannable: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "ConfigMap" {
		t.Fatalf("want only ConfigMap (has list+get), got %+v", got)
	}
}

func TestListScannable_DropsSubresources(t *testing.T) {
	fake := newFake(&metav1.APIResourceList{
		GroupVersion: "apps/v1",
		APIResources: []metav1.APIResource{
			{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: listVerbs("list", "get")},
			{Name: "deployments/status", Kind: "Deployment", Namespaced: true, Verbs: listVerbs("get")},
		},
	})

	res, err := discovery.ListScannable(context.Background(), fake, config.ScopeConfig{})
	got := res.GVRs
	if err != nil {
		t.Fatalf("ListScannable: %v", err)
	}
	if len(got) != 1 || got[0].GVR.Resource != "deployments" {
		t.Fatalf("want only the top-level resource, got %+v", got)
	}
}

func TestListScannable_DropsClusterScopedUnlessIncluded(t *testing.T) {
	fake := newFake(&metav1.APIResourceList{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "namespaces", Kind: "Namespace", Namespaced: false, Verbs: listVerbs("list", "get")},
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: listVerbs("list", "get")},
		},
	})

	res, err := discovery.ListScannable(context.Background(), fake, config.ScopeConfig{IncludeClusterScoped: false})
	got := res.GVRs
	if err != nil {
		t.Fatalf("ListScannable: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "ConfigMap" {
		t.Fatalf("want cluster-scoped dropped by default, got %+v", got)
	}

	res, err = discovery.ListScannable(context.Background(), fake, config.ScopeConfig{IncludeClusterScoped: true})
	got = res.GVRs
	if err != nil {
		t.Fatalf("ListScannable: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want both kinds when cluster-scoped included, got %+v", got)
	}
}

func TestListScannable_KindExcludeAndInclude(t *testing.T) {
	fake := newFake(&metav1.APIResourceList{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: listVerbs("list", "get")},
			{Name: "events", Kind: "Event", Namespaced: true, Verbs: listVerbs("list", "get")},
			{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: listVerbs("list", "get")},
		},
	})

	res, err := discovery.ListScannable(context.Background(), fake, config.ScopeConfig{
		Kinds: config.KindScope{Exclude: []string{"Event"}},
	})
	if err != nil {
		t.Fatalf("ListScannable: %v", err)
	}
	if got := res.GVRs; len(got) != 2 {
		t.Fatalf("want Event excluded, got %+v", got)
	}
	// Served lists every type before the scope filters.
	if len(res.Served) != 3 {
		t.Errorf("Served = %+v, want all 3 types", res.Served)
	}

	res, err = discovery.ListScannable(context.Background(), fake, config.ScopeConfig{
		Kinds: config.KindScope{Exclude: []string{"Event"}, Include: []string{"ConfigMap", "Event"}},
	})
	if err != nil {
		t.Fatalf("ListScannable: %v", err)
	}
	if got := res.GVRs; len(got) != 1 || got[0].Kind != "ConfigMap" {
		t.Fatalf("want only ConfigMap: excludes win over includes, got %+v", got)
	}
}

func TestListScannable_KindMatchIgnoresGroupVersionAndPlural(t *testing.T) {
	// networking.k8s.io/Ingress must match by {Group,Kind}, not by resource
	// plural "ingresses" or version.
	fake := newFake(&metav1.APIResourceList{
		GroupVersion: "networking.k8s.io/v1",
		APIResources: []metav1.APIResource{
			{Name: "ingresses", Kind: "Ingress", Namespaced: true, Verbs: listVerbs("list", "get")},
		},
	})

	res, err := discovery.ListScannable(context.Background(), fake, config.ScopeConfig{
		Kinds: config.KindScope{Exclude: []string{"networking.k8s.io/Ingress"}},
	})
	if err != nil {
		t.Fatalf("ListScannable: %v", err)
	}
	if got := res.GVRs; len(got) != 0 {
		t.Fatalf("want Ingress excluded via group/kind match, got %+v", got)
	}
}

func TestListScannable_DeterministicOrder(t *testing.T) {
	fake := newFake(
		&metav1.APIResourceList{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "services", Kind: "Service", Namespaced: true, Verbs: listVerbs("list", "get")},
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: listVerbs("list", "get")},
			},
		},
		&metav1.APIResourceList{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: listVerbs("list", "get")},
			},
		},
	)

	for i := 0; i < 5; i++ {
		res, err := discovery.ListScannable(context.Background(), fake, config.ScopeConfig{})
		got := res.GVRs
		if err != nil {
			t.Fatalf("ListScannable: %v", err)
		}
		want := []schema.GroupVersionResource{
			{Group: "", Version: "v1", Resource: "configmaps"},
			{Group: "", Version: "v1", Resource: "services"},
			{Group: "apps", Version: "v1", Resource: "deployments"},
		}
		if len(got) != len(want) {
			t.Fatalf("want %d resources, got %d: %+v", len(want), len(got), got)
		}
		for j, w := range want {
			if got[j].GVR != w {
				t.Fatalf("index %d: want %+v, got %+v (full: %+v)", j, w, got[j].GVR, got)
			}
		}
	}
}

// erroringDiscovery wraps a FakeDiscovery and forces
// ServerResourcesForGroupVersion to fail for one groupVersion, simulating a
// dead aggregated API (k8sdiscovery.IsGroupDiscoveryFailedError case).
type erroringDiscovery struct {
	*fakediscovery.FakeDiscovery
	failGV  string
	failErr error
}

func (e *erroringDiscovery) ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	return e.ServerResourcesForGroupVersionWithContext(context.Background(), groupVersion)
}

// client-go >= 0.37 discovers through the context-aware method, so the
// failure must be injected there too or the embedded fake's version runs.
func (e *erroringDiscovery) ServerResourcesForGroupVersionWithContext(ctx context.Context, groupVersion string) (*metav1.APIResourceList, error) {
	if groupVersion == e.failGV {
		return nil, e.failErr
	}
	return e.FakeDiscovery.ServerResourcesForGroupVersionWithContext(ctx, groupVersion)
}

var _ k8sdiscovery.DiscoveryInterface = (*erroringDiscovery)(nil)
