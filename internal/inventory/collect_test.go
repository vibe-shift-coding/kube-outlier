package inventory_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	metadatafake "k8s.io/client-go/metadata/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/discovery"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

var configMapGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
var configMapGVRInfo = discovery.GVRInfo{GVR: configMapGVR, Kind: "ConfigMap", Namespaced: true}

func pom(ns, name, uid string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID(uid)},
	}
}

func listOf(continueToken string, items ...*metav1.PartialObjectMetadata) *metav1.List {
	l := &metav1.List{ListMeta: metav1.ListMeta{Continue: continueToken}}
	for _, it := range items {
		l.Items = append(l.Items, runtime.RawExtension{Object: it})
	}
	return l
}

func newFakeMetadataClient() *metadatafake.FakeMetadataClient {
	return metadatafake.NewSimpleMetadataClient(metadatafake.NewTestScheme())
}

// callCountReactor drives a reactor purely off invocation count. The fake
// metadata client's List path (metadataResourceClient.List ->
// testing.NewRootListAction) rebuilds the recorded action's ListOptions from
// only the label/field selectors and drops Continue/Limit entirely, so
// reactors cannot branch on "which page was requested" by inspecting the
// action — they must track it themselves.
func callCountReactor(fn func(call int) (bool, runtime.Object, error)) clienttesting.ReactionFunc {
	call := 0
	return func(action clienttesting.Action) (bool, runtime.Object, error) {
		call++
		return fn(call)
	}
}

func TestCollectMetadata_Pagination(t *testing.T) {
	client := newFakeMetadataClient()
	client.PrependReactor("list", "configmaps", callCountReactor(func(call int) (bool, runtime.Object, error) {
		if call == 1 {
			return true, listOf("page2", pom("default", "a", "1")), nil
		}
		return true, listOf("", pom("default", "b", "2")), nil
	}))

	snap, err := inventory.CollectMetadata(context.Background(), "c1", client, []discovery.GVRInfo{configMapGVRInfo}, config.ScopeConfig{}, inventory.Options{})
	if err != nil {
		t.Fatalf("CollectMetadata: %v", err)
	}
	if len(snap.Resources) != 2 {
		t.Fatalf("want 2 resources across both pages, got %d: %+v", len(snap.Resources), snap.Resources)
	}
}

func TestCollectMetadata_ExpiredContinueRetriesOnce(t *testing.T) {
	client := newFakeMetadataClient()
	client.PrependReactor("list", "configmaps", callCountReactor(func(call int) (bool, runtime.Object, error) {
		switch call {
		case 1:
			return true, listOf("tok1", pom("default", "a", "1")), nil
		case 2:
			// continuing with tok1 hits an expired token; listAll restarts
			// the whole listing once.
			return true, nil, apierrors.NewResourceExpired("continue token expired")
		case 3:
			return true, listOf("tok1", pom("default", "a", "1")), nil
		default:
			return true, listOf("", pom("default", "b", "2")), nil
		}
	}))

	snap, err := inventory.CollectMetadata(context.Background(), "c1", client, []discovery.GVRInfo{configMapGVRInfo}, config.ScopeConfig{}, inventory.Options{})
	if err != nil {
		t.Fatalf("CollectMetadata: %v", err)
	}
	if len(snap.Errors) != 0 {
		t.Fatalf("want no GVRError after a single successful retry, got %+v", snap.Errors)
	}
	if len(snap.Resources) != 2 {
		t.Fatalf("want the restarted listing's resources, got %d: %+v", len(snap.Resources), snap.Resources)
	}
}

func TestCollectMetadata_ExpiredTwiceRecordsGVRError(t *testing.T) {
	client := newFakeMetadataClient()
	client.PrependReactor("list", "configmaps", callCountReactor(func(call int) (bool, runtime.Object, error) {
		if call == 1 {
			return true, listOf("tok1", pom("default", "a", "1")), nil
		}
		return true, nil, apierrors.NewResourceExpired("continue token expired")
	}))

	snap, err := inventory.CollectMetadata(context.Background(), "c1", client, []discovery.GVRInfo{configMapGVRInfo}, config.ScopeConfig{}, inventory.Options{})
	if err != nil {
		t.Fatalf("CollectMetadata: %v", err)
	}
	if len(snap.Errors) != 1 {
		t.Fatalf("want one GVRError after a second expiry, got %+v", snap.Errors)
	}
	if snap.Errors[0].Forbidden {
		t.Fatalf("expired-token error must not be marked Forbidden: %+v", snap.Errors[0])
	}
}

func TestCollectMetadata_ForbiddenRecordsGVRErrorAndContinues(t *testing.T) {
	client := newFakeMetadataClient()
	client.PrependReactor("list", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("denied"))
	})
	secretGVR := discovery.GVRInfo{GVR: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, Kind: "Secret", Namespaced: true}
	client.PrependReactor("list", "secrets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, listOf("", pom("default", "s1", "9")), nil
	})

	snap, err := inventory.CollectMetadata(context.Background(), "c1", client, []discovery.GVRInfo{configMapGVRInfo, secretGVR}, config.ScopeConfig{}, inventory.Options{})
	if err != nil {
		t.Fatalf("CollectMetadata: %v", err)
	}
	if len(snap.Errors) != 1 || !snap.Errors[0].Forbidden {
		t.Fatalf("want exactly one Forbidden GVRError for configmaps, got %+v", snap.Errors)
	}
	if len(snap.Resources) != 1 || snap.Resources[0].Key.Kind != "Secret" {
		t.Fatalf("want the secrets GVR to still be collected, got %+v", snap.Resources)
	}
}

func TestCollectMetadata_OtherErrorNotForbidden(t *testing.T) {
	client := newFakeMetadataClient()
	client.PrependReactor("list", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("dial tcp: i/o timeout")
	})

	snap, err := inventory.CollectMetadata(context.Background(), "c1", client, []discovery.GVRInfo{configMapGVRInfo}, config.ScopeConfig{}, inventory.Options{})
	if err != nil {
		t.Fatalf("CollectMetadata: %v", err)
	}
	if len(snap.Errors) != 1 || snap.Errors[0].Forbidden {
		t.Fatalf("want one non-forbidden GVRError, got %+v", snap.Errors)
	}
}

func TestCollectMetadata_NamespaceFiltering(t *testing.T) {
	client := newFakeMetadataClient()
	client.PrependReactor("list", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, listOf("",
			pom("kube-system", "a", "1"),
			pom("pr-1234", "b", "2"),
			pom("default", "c", "3"),
		), nil
	})

	scope := config.ScopeConfig{
		Namespaces: config.NamespaceScope{
			Exclude:         []string{"kube-system"},
			ExcludePatterns: []string{"pr-\\d+"},
		},
	}
	snap, err := inventory.CollectMetadata(context.Background(), "c1", client, []discovery.GVRInfo{configMapGVRInfo}, scope, inventory.Options{})
	if err != nil {
		t.Fatalf("CollectMetadata: %v", err)
	}
	if len(snap.Resources) != 1 || snap.Resources[0].Key.Namespace != "default" {
		t.Fatalf("want only the default-namespace item to survive filtering, got %+v", snap.Resources)
	}
}

// With scope.namespaces.include set, namespaced GVRs are listed once per
// included namespace (never cluster-wide), excludes no longer apply, and a
// 403 in one namespace is recorded against that namespace while the others
// are still collected.
func TestCollectMetadata_NamespaceInclude(t *testing.T) {
	client := newFakeMetadataClient()
	var listedNamespaces []string
	client.PrependReactor("list", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		ns := action.GetNamespace()
		listedNamespaces = append(listedNamespaces, ns)
		switch ns {
		case "team-a":
			return true, listOf("", pom("team-a", "debug", "1")), nil
		case "kube-system":
			return true, listOf("", pom("kube-system", "patch", "2")), nil
		default:
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("denied"))
		}
	})
	crbGVR := discovery.GVRInfo{GVR: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, Kind: "ClusterRoleBinding", Namespaced: false}
	var crbNamespace = "unset"
	client.PrependReactor("list", "clusterrolebindings", func(action clienttesting.Action) (bool, runtime.Object, error) {
		crbNamespace = action.GetNamespace()
		return true, listOf("", pom("", "debug-admin", "3")), nil
	})

	scope := config.ScopeConfig{
		IncludeClusterScoped: true,
		Namespaces: config.NamespaceScope{
			Include: []string{"team-b", "team-a", "kube-system", "team-a"},
			Exclude: []string{"kube-system"}, // explicitly included namespaces win
		},
	}
	snap, err := inventory.CollectMetadata(context.Background(), "c1", client, []discovery.GVRInfo{configMapGVRInfo, crbGVR}, scope, inventory.Options{GVRConcurrency: 1})
	if err != nil {
		t.Fatalf("CollectMetadata: %v", err)
	}

	if want := []string{"kube-system", "team-a", "team-b"}; !reflect.DeepEqual(listedNamespaces, want) {
		t.Errorf("configmaps listed in %v, want one call per distinct included namespace %v", listedNamespaces, want)
	}
	if crbNamespace != "" {
		t.Errorf("cluster-scoped GVR listed with namespace %q, want cluster-wide", crbNamespace)
	}
	var got []string
	for _, r := range snap.Resources {
		got = append(got, r.Key.String())
	}
	want := []string{"ConfigMap kube-system/patch", "ConfigMap team-a/debug", "rbac.authorization.k8s.io/ClusterRoleBinding debug-admin"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resources = %v, want %v", got, want)
	}
	if len(snap.Errors) != 1 || snap.Errors[0].Namespace != "team-b" || !snap.Errors[0].Forbidden {
		t.Errorf("errors = %+v, want one forbidden GVRError for namespace team-b", snap.Errors)
	}
}

func TestCollectMetadata_ResourceMapping(t *testing.T) {
	client := newFakeMetadataClient()
	deployGVR := discovery.GVRInfo{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Kind: "Deployment", Namespaced: true}
	client.PrependReactor("list", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, listOf("", pom("shop", "api", "abc-123")), nil
	})

	snap, err := inventory.CollectMetadata(context.Background(), "cluster-eu1", client, []discovery.GVRInfo{deployGVR}, config.ScopeConfig{}, inventory.Options{})
	if err != nil {
		t.Fatalf("CollectMetadata: %v", err)
	}
	if len(snap.Resources) != 1 {
		t.Fatalf("want 1 resource, got %d", len(snap.Resources))
	}
	r := snap.Resources[0]
	if r.Key.Group != "apps" || r.Key.Kind != "Deployment" || r.Key.Namespace != "shop" || r.Key.Name != "api" {
		t.Fatalf("unexpected Key: %+v", r.Key)
	}
	if r.Cluster != "cluster-eu1" {
		t.Fatalf("want Cluster set to the cluster name, got %q", r.Cluster)
	}
	if r.APIVersion != "apps/v1" {
		t.Fatalf("want APIVersion apps/v1, got %q", r.APIVersion)
	}
	if r.UID != "abc-123" {
		t.Fatalf("want UID abc-123, got %q", r.UID)
	}
}

func TestCollectMetadata_SortedByKey(t *testing.T) {
	client := newFakeMetadataClient()
	client.PrependReactor("list", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, listOf("",
			pom("default", "zebra", "1"),
			pom("default", "apple", "2"),
		), nil
	})

	snap, err := inventory.CollectMetadata(context.Background(), "c1", client, []discovery.GVRInfo{configMapGVRInfo}, config.ScopeConfig{}, inventory.Options{})
	if err != nil {
		t.Fatalf("CollectMetadata: %v", err)
	}
	if len(snap.Resources) != 2 || snap.Resources[0].Key.Name != "apple" || snap.Resources[1].Key.Name != "zebra" {
		t.Fatalf("want resources sorted by Key.String(), got %+v", snap.Resources)
	}
}

func TestCollectMetadata_ContextCancellationAbortsCluster(t *testing.T) {
	client := newFakeMetadataClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client.PrependReactor("list", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, ctx.Err()
	})

	_, err := inventory.CollectMetadata(ctx, "c1", client, []discovery.GVRInfo{configMapGVRInfo}, config.ScopeConfig{}, inventory.Options{})
	if err == nil {
		t.Fatal("want error on context cancellation, got nil")
	}
}
