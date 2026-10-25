package analyze

import (
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/wI2L/jsondiff"

	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
)

// ContentDetail is the reference version plus the per-cluster differences for
// one identity (shape frozen in interfaces.md §2.6). The reference is the
// largest group of identical copies; Ambiguous is set when that group is not
// a true majority (more than half the copies) or ties with another group.
type ContentDetail struct {
	MajorityClusters []string            `json:"majorityClusters"` // sorted
	MajorityHash     string              `json:"majorityHash"`
	Ambiguous        bool                `json:"ambiguous,omitempty"`
	Differing        []ClusterDifference `json:"differing"`
}

// ClusterDifference is one cluster whose copy differs from the reference.
type ClusterDifference struct {
	Cluster string    `json:"cluster"`
	Hash    string    `json:"hash"`
	Ops     []FieldOp `json:"ops,omitempty"`
}

// FieldOp is one field-level change, RFC-6902-shaped plus majorityValue for
// human rendering (ADR-0001). Secret values never appear: they are replaced
// by digests before comparison and keyed per run before reporting.
type FieldOp struct {
	Op            string      `json:"op"`
	Path          string      `json:"path"`
	MajorityValue interface{} `json:"majorityValue,omitempty"`
	ClusterValue  interface{} `json:"clusterValue,omitempty"`
}

// CanonicalBody is one cluster's content-comparison copy of a resource (built in
// cmd: Pass B fetch → normalize.CanonicalizeForComparison → normalize.CanonicalHash).
type CanonicalBody struct {
	Cluster string
	Hash    string
	Body    map[string]interface{}
}

// ContentFindings compares content across a live fleet. For each identity in bodies (cmd has
// already filtered to content.kinds present on ≥ majority), it groups clusters by
// Hash, picks the consensus (largest group, deterministic tie-break), and
// jsondiffs each minority copy against it. Identities where every cluster
// agrees produce no finding. Returns ClassContentDiffers findings sorted globally.
func ContentFindings(bodies map[identity.ResourceKey][]CanonicalBody, presence map[identity.ResourceKey]*Presence, creation map[identity.ResourceKey]map[string]metav1.Time, now time.Time) []Finding {
	keys := make([]identity.ResourceKey, 0, len(bodies))
	for k := range bodies {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })

	var findings []Finding
	for _, key := range keys {
		copies := bodies[key]
		if len(copies) < 2 {
			continue // nothing to compare
		}
		consensus, consensusHash, ambiguous := consensusGroup(copies)
		inConsensus := make(map[string]bool, len(consensus))
		for _, c := range consensus {
			inConsensus[c.Cluster] = true
		}

		var divergent []ClusterDifference
		var divergentClusters []string
		consensusBody := consensus[0].Body
		for _, c := range copies {
			if inConsensus[c.Cluster] {
				continue
			}
			divergentClusters = append(divergentClusters, c.Cluster)
			divergent = append(divergent, ClusterDifference{
				Cluster: c.Cluster,
				Hash:    c.Hash,
				Ops:     diffOps(consensusBody, c.Body),
			})
		}
		if len(divergent) == 0 {
			continue // every copy matches: no finding
		}
		sort.Slice(divergent, func(i, j int) bool { return divergent[i].Cluster < divergent[j].Cluster })
		sort.Strings(divergentClusters)

		consensusNames := make([]string, 0, len(consensus))
		for _, c := range consensus {
			consensusNames = append(consensusNames, c.Cluster)
		}
		sort.Strings(consensusNames)

		msg := fmt.Sprintf("%s content differs on %d/%d clusters from the majority version",
			keyDisplay(key), len(divergent), len(copies))
		if ambiguous {
			msg = fmt.Sprintf("%s content differs on %d/%d clusters from the most common version (no majority)",
				keyDisplay(key), len(divergent), len(copies))
		}

		findings = append(findings, Finding{
			Class:    ClassContentDiffers,
			Key:      key,
			Clusters: divergentClusters,
			Presence: presence[key],
			// A fixed zero score: the comparison weighs no evidence (ADR-0002).
			// Reasons is [] like every other finding's, never null.
			Score: Score{Reasons: []ScoreReason{}},
			Content: &ContentDetail{
				MajorityClusters: consensusNames,
				MajorityHash:     consensusHash,
				Ambiguous:        ambiguous,
				Differing:        divergent,
			},
			CreationTimestamps: copyTimestamps(creation[key]),
			AgeDays:            oldestAgeDays(now, creation[key]),
			Message:            msg,
		})
	}

	SortFindings(findings)
	return findings
}

// consensusGroup returns the largest group of copies sharing a hash, its hash,
// and whether the choice was ambiguous: ≥2 groups tied for largest, or the
// largest group holds no more than half the copies (a plurality, not a
// majority). Ties are broken by the lexicographically-first sorted cluster
// list (ADR-0006).
func consensusGroup(copies []CanonicalBody) (group []CanonicalBody, hash string, ambiguous bool) {
	byHash := map[string][]CanonicalBody{}
	var order []string
	for _, c := range copies {
		if _, seen := byHash[c.Hash]; !seen {
			order = append(order, c.Hash)
		}
		byHash[c.Hash] = append(byHash[c.Hash], c)
	}
	if len(byHash) == 1 {
		return copies, copies[0].Hash, false
	}

	maxLen := 0
	for _, g := range byHash {
		if len(g) > maxLen {
			maxLen = len(g)
		}
	}
	// Candidate groups of the maximal size, tie-broken by first cluster name.
	var candidates []string
	for _, h := range order {
		if len(byHash[h]) == maxLen {
			candidates = append(candidates, h)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return firstCluster(byHash[candidates[i]]) < firstCluster(byHash[candidates[j]])
	})
	winner := candidates[0]
	return byHash[winner], winner, len(candidates) > 1 || 2*maxLen <= len(copies)
}

func firstCluster(group []CanonicalBody) string {
	names := make([]string, 0, len(group))
	for _, c := range group {
		names = append(names, c.Cluster)
	}
	sort.Strings(names)
	return names[0]
}

// diffOps computes the field-level changes transforming the consensus body
// into the cluster's copy, as our own FieldOp type. jsondiff runs with no
// options (no factorization), so only add/remove/replace ops are produced and
// OldValue is populated for direct mapping.
func diffOps(consensus, cluster map[string]interface{}) []FieldOp {
	patch, err := jsondiff.Compare(consensus, cluster)
	if err != nil {
		return nil
	}
	ops := make([]FieldOp, 0, len(patch))
	for _, op := range patch {
		ops = append(ops, FieldOp{
			Op:            op.Type,
			Path:          op.Path,
			MajorityValue: op.OldValue, // reference value; nil for "add"
			ClusterValue:  op.Value,    // cluster value; nil for "remove"
		})
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Path < ops[j].Path })
	return ops
}
