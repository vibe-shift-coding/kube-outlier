package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/connect"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
	"github.com/vibe-shift-coding/kube-outlier/internal/normalize"
	"github.com/vibe-shift-coding/kube-outlier/internal/report"
)

// contentFetchConcurrency bounds the Pass B Gets in flight across the fleet.
// The per-cluster client rate limit (connect) still applies on top.
const contentFetchConcurrency = 8

// sortedStrings returns a sorted copy of s.
func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// contentResult is what one content comparison produced.
type contentResult struct {
	findings []analyze.Finding
	// unread are the copies that could not be read (a forbidden get, a
	// timeout). They make the run partial: a copy left out silently could
	// hide a difference.
	unread []report.SkippedObject
	// skipReason is non-empty when the comparison could not run at all, so
	// callers can refuse a content gate that could never fail.
	skipReason string
	// unmatchedIgnore are the content.ignoreFields paths that matched no
	// field in any copy read: usually a typo, which would ignore nothing.
	unmatchedIgnore []string
}

// runContentComparison computes content-comparison findings across the live
// fleet, with full field diffs. Pass B (full-object Gets) runs only for
// content.kinds resources that are not system objects, fail-soft per fetch.
func runContentComparison(ctx context.Context, results []clusterResult, cfg *config.Config, now time.Time) (contentResult, error) {
	kinds, err := contentKindSet(cfg.Content.Kinds)
	if err != nil {
		return contentResult{}, exitErrorf(1, "%v", err)
	}
	if len(results) < 2 {
		fmt.Fprintln(os.Stderr, "warning: --compare-content needs 2+ reachable clusters; skipping the content comparison")
		return contentResult{skipReason: "it needs 2+ reachable clusters; a baseline holds no content"}, nil
	}
	if contentKindsInScope(os.Stderr, results, cfg) == 0 {
		return contentResult{skipReason: "no content.kinds entry matches a resource type the scan listed"}, nil
	}
	res := compareLive(ctx, results, cfg, kinds, now)
	for _, p := range res.unmatchedIgnore {
		fmt.Fprintf(os.Stderr, "warning: content.ignoreFields: %q matched no field in any compared copy (check the spelling: dot-separated keys, [*] or [N] for list elements)\n", p)
	}
	if err := keySecretDigests(res.findings, cfg.SensitiveKindSet()); err != nil {
		return contentResult{}, exitErrorf(1, "%v", err)
	}
	return res, nil
}

// fetchJob is one Pass B Get: an identity on a cluster.
type fetchJob struct {
	key     identity.ResourceKey
	cluster string
	gvr     schema.GroupVersionResource
	clients *connect.Clients
}

// fetchAll runs the jobs with bounded concurrency and returns the canonical
// bodies that could be fetched, keyed by identity, each list sorted by
// cluster, plus the copies that could not be read, sorted by cluster and
// identity. A copy deleted since Pass A is neither: it no longer exists.
// Kinds in sensitive have every value hashed, not just a Secret's data.
// matched holds the ignoreFields paths found in at least one copy.
func fetchAll(ctx context.Context, jobs []fetchJob, ignoreFields []string, sensitive map[config.KindRef]bool) (bodies map[identity.ResourceKey][]analyze.CanonicalBody, unread []report.SkippedObject, matched map[string]bool) {
	out := map[identity.ResourceKey][]analyze.CanonicalBody{}
	matched = map[string]bool{}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(contentFetchConcurrency)
	for _, j := range jobs {
		g.Go(func() error {
			cb, found, err := fetchCanonical(gctx, j.clients, j.gvr, j.key, j.cluster, ignoreFields, isSensitive(sensitive, j.key))
			mu.Lock()
			defer mu.Unlock()
			for _, p := range found {
				matched[p] = true
			}
			switch {
			case err == nil:
				out[j.key] = append(out[j.key], cb)
			case apierrors.IsNotFound(err):
				slog.Debug("content comparison: object deleted since it was listed", "cluster", j.cluster, "resource", j.key.String())
			default:
				reason := err.Error()
				if apierrors.IsForbidden(err) {
					reason = "forbidden"
				}
				unread = append(unread, report.SkippedObject{Cluster: j.cluster, Resource: j.key, Reason: reason, Forbidden: apierrors.IsForbidden(err)})
			}
			return nil
		})
	}
	_ = g.Wait()
	for k := range out {
		sort.Slice(out[k], func(a, b int) bool { return out[k][a].Cluster < out[k][b].Cluster })
	}
	sort.Slice(unread, func(a, b int) bool {
		if unread[a].Cluster != unread[b].Cluster {
			return unread[a].Cluster < unread[b].Cluster
		}
		return unread[a].Resource.String() < unread[b].Resource.String()
	})
	return out, unread, matched
}

// compareLive fetches and canonicalizes every comparison-eligible identity's copies
// across the fleet, then hands them to analyze.ContentFindings. System objects
// are never compared: like kube-root-ca.crt, which holds each cluster's own CA,
// they legitimately differ per cluster and are never findings (ADR-0005).
func compareLive(ctx context.Context, results []clusterResult, cfg *config.Config, kinds map[config.KindRef]bool, now time.Time) contentResult {
	snaps := snapshotsOf(results)
	matrix := analyze.BuildPresence(snaps)
	systemKeys := analyze.SystemObjectKeys(snaps)
	gvrByCluster := gvrByKindPerCluster(results)
	creationByCluster := creationPerCluster(results)
	clientsByCluster := clientsPerCluster(results)

	// Eligible: present on at least commonThreshold of the fleet (and on 2+).
	minClusters := int(cfg.Analysis.CommonThreshold*float64(len(matrix.Clusters)) + 0.999) // ceil
	var jobs []fetchJob
	for key, clusterSet := range matrix.ByKey {
		kr := config.KindRef{Group: key.Group, Kind: key.Kind}
		if !kinds[kr] || systemKeys[key] {
			continue
		}
		if len(clusterSet) < 2 || len(clusterSet) < minClusters {
			continue // not present on enough clusters to have a meaningful reference version
		}
		for cluster := range clusterSet {
			gvr, ok := gvrByCluster[cluster][kr]
			c := clientsByCluster[cluster]
			if !ok || c == nil {
				continue
			}
			jobs = append(jobs, fetchJob{key: key, cluster: cluster, gvr: gvr, clients: c})
		}
	}

	bodies, unread, matched := fetchAll(ctx, jobs, cfg.Content.IgnoreFields, cfg.SensitiveKindSet())
	res := contentResult{unread: unread}
	if len(jobs) > 0 && len(unread) == len(jobs) {
		res.skipReason = fmt.Sprintf("none of the %d copies it needed could be read", len(jobs))
	}
	if len(bodies) > 0 {
		for _, p := range cfg.Content.IgnoreFields {
			if !matched[p] {
				res.unmatchedIgnore = append(res.unmatchedIgnore, p)
			}
		}
	}
	presence := map[identity.ResourceKey]*analyze.Presence{}
	creation := map[identity.ResourceKey]map[string]metav1.Time{}
	for key, copies := range bodies {
		if len(copies) < 2 {
			delete(bodies, key)
			continue
		}
		presentOn := make([]string, 0, len(copies))
		ts := map[string]metav1.Time{}
		for _, c := range copies {
			presentOn = append(presentOn, c.Cluster)
			if t, ok := creationByCluster[c.Cluster][key]; ok {
				ts[c.Cluster] = t
			}
		}
		presence[key] = &analyze.Presence{
			PresentOn:    sortedStrings(presentOn),
			MissingFrom:  matrix.MissingFrom(key),
			PresentCount: len(matrix.PresentOn(key)),
			FleetSize:    len(matrix.Clusters),
			Ratio:        float64(len(matrix.PresentOn(key))) / float64(len(matrix.Clusters)),
		}
		creation[key] = ts
	}

	res.findings = analyze.ContentFindings(bodies, presence, creation, now)
	return res
}

// fetchCanonical is Pass B for one identity on one cluster: Get → normalize →
// hash. It also returns the ignoreFields paths present in the copy. The error
// keeps the API status, so callers can tell a deleted object (NotFound) from
// one they may not read (Forbidden).
func fetchCanonical(ctx context.Context, c *connect.Clients, gvr schema.GroupVersionResource, key identity.ResourceKey, cluster string, ignoreFields []string, sensitive bool) (analyze.CanonicalBody, []string, error) {
	body, err := inventory.FetchBody(ctx, c.Dynamic, gvr, key)
	if err != nil {
		return analyze.CanonicalBody{}, nil, err
	}
	var found []string
	for _, p := range ignoreFields {
		if normalize.PathMatches(body, p) {
			found = append(found, p)
		}
	}
	canon := normalize.CanonicalizeForComparison(body, key, ignoreFields, sensitive)
	hash, err := normalize.CanonicalHash(canon)
	if err != nil {
		return analyze.CanonicalBody{}, found, fmt.Errorf("hashing %s: %w", key.String(), err)
	}
	return analyze.CanonicalBody{Cluster: cluster, Hash: hash, Body: canon}, found, nil
}

// isSensitive reports whether key's kind is in sensitive: the core Secret or a
// sensitive.kinds kind (config.SensitiveKindSet).
func isSensitive(sensitive map[config.KindRef]bool, key identity.ResourceKey) bool {
	return sensitive[config.KindRef{Group: key.Group, Kind: key.Kind}]
}

// keySecretDigests replaces every digest that could reveal a Secret value
// before it reaches a report. Comparison hashes Secret values with plain
// SHA-256 (stable, so copies can be grouped), and a plain SHA-256 of a short
// password is easily reversed by guessing. So for findings on a sensitive kind
// (Secrets and sensitive.kinds) the value digests in ops and the whole-body hashes are re-keyed with HMAC-SHA256
// under a random key that lives only for this run: equal values still match
// within one report, but nothing can be checked against a guess.
func keySecretDigests(findings []analyze.Finding, sensitive map[config.KindRef]bool) error {
	var key []byte
	for i := range findings {
		f := &findings[i]
		if f.Content == nil || !isSensitive(sensitive, f.Key) {
			continue
		}
		if key == nil {
			key = make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				return fmt.Errorf("generating the per-run key for Secret digests: %w", err)
			}
		}
		f.Content.MajorityHash = keyedDigest(key, f.Content.MajorityHash)
		for j := range f.Content.Differing {
			d := &f.Content.Differing[j]
			d.Hash = keyedDigest(key, d.Hash)
			for k := range d.Ops {
				d.Ops[k].MajorityValue = keyDigestsIn(key, d.Ops[k].MajorityValue)
				d.Ops[k].ClusterValue = keyDigestsIn(key, d.Ops[k].ClusterValue)
			}
		}
	}
	return nil
}

// keyDigestsIn rewrites every "sha256:" string inside v (a JSON value: a
// string, or the map or slice an op adds or removes whole) with its keyed
// digest.
func keyDigestsIn(key []byte, v interface{}) interface{} {
	switch t := v.(type) {
	case string:
		if strings.HasPrefix(t, "sha256:") {
			return keyedDigest(key, t)
		}
		return t
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, e := range t {
			out[k] = keyDigestsIn(key, e)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, e := range t {
			out[i] = keyDigestsIn(key, e)
		}
		return out
	default:
		return v
	}
}

// keyedDigest is "hmac-sha256:" plus the first 16 hex characters of
// HMAC-SHA256(key, s): enough to tell values apart within a report.
func keyedDigest(key []byte, s string) string {
	if s == "" {
		return s
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(s))
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))[:16]
}

// contentKindSet parses the configured content.kinds into a lookup set.
func contentKindSet(kinds []string) (map[config.KindRef]bool, error) {
	set := make(map[config.KindRef]bool, len(kinds))
	for _, k := range kinds {
		kr, err := config.ParseKindRef(k)
		if err != nil {
			return nil, fmt.Errorf("content.kinds: %w", err)
		}
		set[kr] = true
	}
	return set, nil
}

// gvrByKindPerCluster maps, per cluster, {Group,Kind} → served GVR (for Pass B).
func gvrByKindPerCluster(results []clusterResult) map[string]map[config.KindRef]schema.GroupVersionResource {
	out := make(map[string]map[config.KindRef]schema.GroupVersionResource, len(results))
	for _, r := range results {
		m := make(map[config.KindRef]schema.GroupVersionResource, len(r.gvrs))
		for _, gi := range r.gvrs {
			m[config.KindRef{Group: gi.GVR.Group, Kind: gi.Kind}] = gi.GVR
		}
		out[r.snap.Cluster] = m
	}
	return out
}

// clientsPerCluster maps cluster name → the client bundle that scanned it.
func clientsPerCluster(results []clusterResult) map[string]*connect.Clients {
	out := make(map[string]*connect.Clients, len(results))
	for _, r := range results {
		out[r.snap.Cluster] = r.clients
	}
	return out
}

// creationPerCluster maps, per cluster, identity → creation timestamp.
func creationPerCluster(results []clusterResult) map[string]map[identity.ResourceKey]metav1.Time {
	out := make(map[string]map[identity.ResourceKey]metav1.Time, len(results))
	for _, r := range results {
		m := make(map[identity.ResourceKey]metav1.Time, len(r.snap.Resources))
		for _, res := range r.snap.Resources {
			m[res.Key] = res.CreationTimestamp
		}
		out[r.snap.Cluster] = m
	}
	return out
}
