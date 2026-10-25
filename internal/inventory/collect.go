// Package inventory collects metadata-only snapshots of in-scope resources
// from one cluster (Pass A). Pass B (targeted full-object fetch) lives in
// fetch.go.
package inventory

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"

	"golang.org/x/sync/errgroup"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/discovery"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
)

// Resource is the metadata-only record from Pass A. One per (cluster, object).
// The kubectl last-applied-configuration annotation is kept with an empty
// value: its presence is evidence, its value can hold Secret data.
type Resource struct {
	Key               identity.ResourceKey
	Cluster           string // connect.ClusterRef.Name
	APIVersion        string // as served on THIS cluster, e.g. "apps/v1" (informational only)
	UID               string
	Labels            map[string]string
	Annotations       map[string]string
	OwnerReferences   []metav1.OwnerReference
	ManagedFields     []metav1.ManagedFieldsEntry
	CreationTimestamp metav1.Time
}

// Snapshot is everything collected from one cluster in one run.
type Snapshot struct {
	Cluster   string
	Resources []Resource
	Errors    []GVRError // GVRs that could not be listed (forbidden, timeout, ...)
}

// GVRError records one GVR that could not be listed on this cluster.
type GVRError struct {
	GVR       schema.GroupVersionResource
	Namespace string `json:",omitempty"` // "" = cluster-wide list; set when scope.namespaces.include lists per namespace
	Err       string
	Forbidden bool // true for 403: report as "forbidden, skipped", never fatal
}

// Options tunes pagination and concurrency for CollectMetadata.
type Options struct {
	PageLimit      int64 // default 500
	GVRConcurrency int   // default 8 (per cluster)
}

const (
	defaultPageLimit      = 500
	defaultGVRConcurrency = 8
)

// CollectMetadata is Pass A: metadata-only listing of every in-scope GVR on
// one cluster. It never fetches full object bodies and never keeps Secret
// data: the metadata client returns no data fields, and the one annotation
// that can embed them (last-applied-configuration) is emptied on the way in.
//
// With scope.namespaces.include set, namespaced GVRs are listed once per
// included namespace (so namespace-scoped RBAC suffices) and the namespace
// exclude lists do not apply; cluster-scoped GVRs are always listed
// cluster-wide.
func CollectMetadata(ctx context.Context, cluster string, mc metadata.Interface, gvrs []discovery.GVRInfo, scope config.ScopeConfig, opts Options) (*Snapshot, error) {
	if opts.PageLimit <= 0 {
		opts.PageLimit = defaultPageLimit
	}
	if opts.GVRConcurrency <= 0 {
		opts.GVRConcurrency = defaultGVRConcurrency
	}

	include := dedupSorted(scope.Namespaces.Include)
	keep := func(string) bool { return true }
	if len(include) == 0 {
		excludePatterns, err := compileExcludePatterns(scope.Namespaces.ExcludePatterns)
		if err != nil {
			return nil, err
		}
		excludedNamespaces := make(map[string]bool, len(scope.Namespaces.Exclude))
		for _, ns := range scope.Namespaces.Exclude {
			excludedNamespaces[ns] = true
		}
		keep = func(ns string) bool { return !excludedNamespaces[ns] && !matchesAny(excludePatterns, ns) }
	}

	// One listing task per GVR, or per (GVR, namespace) for namespaced GVRs
	// when an include list restricts the scan.
	type listTask struct {
		gi        discovery.GVRInfo
		namespace string // "" = cluster-wide
	}
	var tasks []listTask
	for _, gi := range gvrs {
		if gi.Namespaced && len(include) > 0 {
			for _, ns := range include {
				tasks = append(tasks, listTask{gi: gi, namespace: ns})
			}
			continue
		}
		tasks = append(tasks, listTask{gi: gi})
	}

	type gvrResult struct {
		resources []Resource
		gvrErr    *GVRError
	}
	results := make([]gvrResult, len(tasks))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opts.GVRConcurrency)
	for i, t := range tasks {
		g.Go(func() error {
			resources, gvrErr := collectOne(gctx, mc, t.gi, t.namespace, opts.PageLimit, keep)
			results[i] = gvrResult{resources: resources, gvrErr: gvrErr}
			return gctx.Err()
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("collecting metadata for cluster %s: %w", cluster, err)
	}

	snap := &Snapshot{Cluster: cluster}
	for _, r := range results {
		for _, res := range r.resources {
			res.Cluster = cluster
			snap.Resources = append(snap.Resources, res)
		}
		if r.gvrErr != nil {
			snap.Errors = append(snap.Errors, *r.gvrErr)
		}
	}
	sort.Slice(snap.Resources, func(i, j int) bool {
		return snap.Resources[i].Key.String() < snap.Resources[j].Key.String()
	})

	return snap, nil
}

// collectOne lists one GVR (cluster-wide when namespace is "", else within
// that namespace) to completion, paginating and retrying once on an expired
// continue token. keep filters items by namespace. Only ctx cancellation is
// returned as an error; every other failure is captured in the *GVRError.
func collectOne(ctx context.Context, mc metadata.Interface, gi discovery.GVRInfo, namespace string, pageLimit int64, keep func(string) bool) ([]Resource, *GVRError) {
	apiVersion := gi.GVR.GroupVersion().String()

	list, err := listAll(ctx, mc, gi.GVR, namespace, pageLimit)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil
		}
		return nil, &GVRError{GVR: gi.GVR, Namespace: namespace, Err: err.Error(), Forbidden: apierrors.IsForbidden(err)}
	}

	out := make([]Resource, 0, len(list))
	for _, item := range list {
		ns := item.Namespace
		if !keep(ns) {
			continue
		}
		out = append(out, Resource{
			Key: identity.ResourceKey{
				Group:     gi.GVR.Group,
				Kind:      gi.Kind,
				Namespace: ns,
				Name:      item.Name,
			},
			APIVersion:        apiVersion,
			UID:               string(item.UID),
			Labels:            item.Labels,
			Annotations:       redactLastApplied(item.Annotations),
			OwnerReferences:   item.OwnerReferences,
			ManagedFields:     item.ManagedFields,
			CreationTimestamp: item.CreationTimestamp,
		})
	}
	return out, nil
}

// lastAppliedAnnotation is written by `kubectl apply` and holds the full
// applied manifest — for a Secret, its data and stringData values.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// redactLastApplied returns annotations with the last-applied-configuration
// value emptied. Only the key's presence is evidence (ownership signal
// kubectl.last-applied); the value would carry Secret values into snapshots
// and memory. The input map is never mutated.
func redactLastApplied(annotations map[string]string) map[string]string {
	if _, ok := annotations[lastAppliedAnnotation]; !ok {
		return annotations
	}
	out := make(map[string]string, len(annotations))
	for k, v := range annotations {
		out[k] = v
	}
	out[lastAppliedAnnotation] = ""
	return out
}

// listAll paginates through every page of gvr, retrying the whole listing
// once if a continue token expires (410 Gone) mid-stream.
func listAll(ctx context.Context, mc metadata.Interface, gvr schema.GroupVersionResource, namespace string, pageLimit int64) ([]metav1.PartialObjectMetadata, error) {
	items, err := listPages(ctx, mc, gvr, namespace, pageLimit)
	if err != nil && apierrors.IsResourceExpired(err) {
		items, err = listPages(ctx, mc, gvr, namespace, pageLimit)
	}
	return items, err
}

func listPages(ctx context.Context, mc metadata.Interface, gvr schema.GroupVersionResource, namespace string, pageLimit int64) ([]metav1.PartialObjectMetadata, error) {
	var out []metav1.PartialObjectMetadata
	continueToken := ""
	for {
		list, err := mc.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{
			Limit:    pageLimit,
			Continue: continueToken,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, list.Items...)
		continueToken = list.Continue
		if continueToken == "" {
			return out, nil
		}
	}
}

func compileExcludePatterns(patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile("^(?:" + p + ")$")
		if err != nil {
			return nil, fmt.Errorf("scope.namespaces.excludePatterns: %q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func matchesAny(patterns []*regexp.Regexp, s string) bool {
	for _, re := range patterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// dedupSorted returns the distinct values of s, sorted.
func dedupSorted(s []string) []string {
	seen := make(map[string]bool, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
