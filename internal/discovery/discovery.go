// Package discovery enumerates the served, listable, in-scope resource
// types on one cluster.
package discovery

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sdiscovery "k8s.io/client-go/discovery"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
)

// FailedGroup is an API group version whose discovery failed on a cluster
// (an aggregated API whose backing service is down, for example). Its types
// are unknown there, so they are neither scanned nor counted as absent.
type FailedGroup struct {
	GroupVersion schema.GroupVersion
	Err          string
}

// GVRInfo is one served, listable resource type.
type GVRInfo struct {
	GVR        schema.GroupVersionResource
	Kind       string
	Namespaced bool
}

// Result is what ListScannable found on one cluster.
type Result struct {
	// GVRs are the served, listable, in-scope types: the ones a scan lists.
	GVRs []GVRInfo
	// Served is every served, listable type before the scope filters, so a
	// caller can tell a misspelled kind from one the scope leaves out.
	Served []GVRInfo
	// Failed are the API group versions whose discovery failed, sorted.
	Failed []FailedGroup
}

// ListScannable enumerates served, listable, in-scope resource types on one
// cluster via ServerPreferredResources (one, preferred, version per
// group/kind, so identities stay version-agnostic). client-go discovery takes
// no context, so its requests are bounded by the client's request timeout;
// ctx is checked before starting.
//
// A group whose discovery fails does not stop the run: it is returned in
// Result.Failed (sorted), unless config.DiscoveryOptional says losing it costs
// nothing, and the caller reports it as skipped.
func ListScannable(ctx context.Context, d k8sdiscovery.DiscoveryInterface, scope config.ScopeConfig) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	var failed []FailedGroup
	lists, err := k8sdiscovery.ServerPreferredResources(d)
	if err != nil {
		var groupErr *k8sdiscovery.ErrGroupDiscoveryFailed
		if !errors.As(err, &groupErr) {
			return Result{}, err
		}
		for gv, gerr := range groupErr.Groups {
			if config.DiscoveryOptional(gv.Group) {
				slog.Debug("ignoring failed discovery of an optional API group", "groupVersion", gv.String(), "error", gerr)
				continue
			}
			failed = append(failed, FailedGroup{GroupVersion: gv, Err: gerr.Error()})
		}
		sort.Slice(failed, func(i, j int) bool { return failed[i].GroupVersion.String() < failed[j].GroupVersion.String() })
	}

	exclude, err := parseKindRefSet(scope.Kinds.Exclude)
	if err != nil {
		return Result{}, err
	}
	include, err := parseKindRefSet(scope.Kinds.Include)
	if err != nil {
		return Result{}, err
	}

	var out, served []GVRInfo
	for _, list := range lists {
		if list == nil {
			continue
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			slog.Warn("skipping resource list with unparseable groupVersion", "groupVersion", list.GroupVersion, "error", err)
			continue
		}
		for _, r := range list.APIResources {
			if !hasVerbs(r.Verbs, "list", "get") {
				continue
			}
			if strings.Contains(r.Name, "/") {
				continue
			}
			info := GVRInfo{
				GVR:        gv.WithResource(r.Name),
				Kind:       r.Kind,
				Namespaced: r.Namespaced,
			}
			served = append(served, info)
			if !r.Namespaced && !scope.IncludeClusterScoped {
				continue
			}
			kindRef := config.KindRef{Group: gv.Group, Kind: r.Kind}
			if exclude[kindRef] {
				continue
			}
			if len(include) > 0 && !include[kindRef] {
				continue
			}
			out = append(out, info)
		}
	}

	slog.Debug("discovered scannable resource types", "count", len(out), "served", len(served))
	sortGVRs(out)
	sortGVRs(served)

	return Result{GVRs: out, Served: served, Failed: failed}, nil
}

// sortGVRs orders types by group, then resource, for deterministic output.
func sortGVRs(gvrs []GVRInfo) {
	sort.Slice(gvrs, func(i, j int) bool {
		if gvrs[i].GVR.Group != gvrs[j].GVR.Group {
			return gvrs[i].GVR.Group < gvrs[j].GVR.Group
		}
		return gvrs[i].GVR.Resource < gvrs[j].GVR.Resource
	})
}

func hasVerbs(verbs metav1.Verbs, want ...string) bool {
	have := make(map[string]bool, len(verbs))
	for _, v := range verbs {
		have[v] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

func parseKindRefSet(refs []string) (map[config.KindRef]bool, error) {
	set := make(map[config.KindRef]bool, len(refs))
	for _, s := range refs {
		ref, err := config.ParseKindRef(s)
		if err != nil {
			return nil, err
		}
		set[ref] = true
	}
	return set, nil
}
