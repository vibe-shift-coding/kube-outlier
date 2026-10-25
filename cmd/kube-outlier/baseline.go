package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/baseline"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/connect"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
)

func newBaselineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "baseline",
		Short: "Record what the fleet looks like, for later single-cluster scans",
		Long: `A baseline is a small, committable file recording, for each resource, how many
clusters had it and how it was owned — never manifests or Secret values. It
does list cluster names and every resource's kind, namespace and name. Scan
one cluster with --baseline and it gets the fleet presence signal from the
file, without contacting the other clusters.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newBaselineGenerateCmd())
	return cmd
}

func newBaselineGenerateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Scan the fleet and write a baseline file (presence counts and ownership tallies, never manifests or Secret values)",
		Long: `Generate scans every requested cluster and writes the baseline. It records the
exact scan scope (namespace and kind exclusions, -n, --include-cluster-scoped);
a later scan --baseline with a different scope stops rather than compare
unlike with unlike, so generate with the same scope flags you scan with.

Hand-written exceptions in an existing file at --out are kept. To add one, put
an exceptions: list at the top level of the file; each entry has a resource
(as --why takes it), a reason, approvedBy and, optionally, an expires date.

Exit codes: 0 written; 1 fatal; 2 written, but a cluster was skipped or a
kind left out because it could not be listed everywhere.`,
		Example: `  # From the whole fleet
  kube-outlier baseline generate --contexts cluster-eu1,cluster-us1,cluster-ap1 --out baseline.yaml

  # Only the namespaces a team scans, with the same -n it scans with
  kube-outlier baseline generate --contexts cluster-eu1,cluster-us1,cluster-ap1 -n team-a --out team-a.yaml`,
		Args: cobra.NoArgs,
		RunE: runBaselineGenerate,
	}
	addFleetFlags(cmd)
	// --out, not -o: -o/--output means report format on scan and clusters.
	// Extension .json emits JSON of the identical structure; "-" = stdout.
	cmd.Flags().String("out", "baseline.yaml", `output path ("-" = stdout; a .json extension writes JSON)`)
	cmd.Flags().StringSliceP("namespaces", "n", nil, "record only these namespaces, comma-separated or repeated (alias --namespace)")
	cmd.Flags().Bool("include-cluster-scoped", false, "also record cluster-scoped kinds (needed to scan with --include-cluster-scoped against it)")
	cmd.Flags().Bool("discard-exceptions", false, "do not keep the exceptions block of an existing file at --out")
	return cmd
}

func runBaselineGenerate(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	refs, err := connect.Resolve(cfg)
	if err != nil {
		return exitErrorf(1, "%v", err)
	}
	refs = connect.ProbeAll(cmd.Context(), refs, cfg.Clusters.ProbeTimeout)

	results := scanAllClusters(cmd.Context(), refs, cfg)
	if len(results) == 0 {
		return noReachableClusters("no reachable clusters: a baseline needs at least one contributing cluster", refs)
	}
	warnEmptyNamespaces(os.Stderr, results, cfg)
	warnUnmatchedKinds(os.Stderr, results, cfg)
	if len(results) == 1 {
		fmt.Fprintln(os.Stderr, "warning: single-cluster baseline — presence ratios carry no rarity evidence (fleet modifiers stay off when scanning against it)")
	}

	// Clean-listing rule: only kinds cleanly listable (served, no GVRError)
	// on EVERY contributing cluster get entries, keeping Entry.Ratio exactly
	// ClusterCount/Provenance.ClusterCount (interfaces.md §2.7).
	included, excludedKinds := cleanKinds(results)
	for _, ex := range excludedKinds {
		fmt.Fprintf(os.Stderr, "baseline: kind %s excluded (%s)\n", ex.kind, ex.reason)
	}

	snaps := snapshotsOf(results)
	matrix := analyze.BuildPresence(snaps)
	tallies := analyze.OwnershipTallies(snaps, cfg.Analysis)
	// System objects are never findings, so they never become entries either:
	// a version-skewed target would otherwise see "missing" built-ins.
	systemKeys := analyze.SystemObjectKeys(snaps)

	var entries []baseline.Entry
	for key := range matrix.ByKey {
		if !included[config.KindRef{Group: key.Group, Kind: key.Kind}] || systemKeys[key] {
			continue
		}
		count := len(matrix.PresentOn(key))
		entries = append(entries, baseline.Entry{
			Key:              baseline.ResourceRef(key),
			ClusterCount:     count,
			Ratio:            float64(count) / float64(len(results)),
			OwnershipSummary: tallies[key],
		})
	}

	outPath, _ := cmd.Flags().GetString("out")
	exceptions, err := carriedExceptions(cmd, outPath)
	if err != nil {
		return err
	}

	clusterNames := make([]string, 0, len(results))
	for _, r := range results {
		clusterNames = append(clusterNames, r.snap.Cluster)
	}
	b := baseline.New(version(), time.Now(), clusterNames,
		baseline.FingerprintScope(cfg.Scope), entries, exceptions)

	if err := writeBaseline(b, outPath); err != nil {
		return exitErrorf(1, "%v", err)
	}
	if outPath != "-" {
		clusters := "clusters"
		if len(results) == 1 {
			clusters = "cluster"
		}
		fmt.Fprintf(os.Stderr, "baseline: wrote %s (%d entries from %d %s)\n", outPath, len(entries), len(results), clusters)
	}

	// Exit 2 = baseline written but partial: a requested cluster was skipped
	// or a kind was excluded by the clean-listing rule.
	requested := len(refs)
	if len(results) < requested || len(excludedKinds) > 0 {
		return exitSilently(2)
	}
	return nil
}

type excludedKind struct {
	kind   string
	reason string
}

// cleanKinds partitions the union of all clusters' served kinds into those
// cleanly listable everywhere (included) and those excluded, each with the
// first offending cluster as the reason.
func cleanKinds(results []clusterResult) (map[config.KindRef]bool, []excludedKind) {
	type clusterKinds struct {
		cluster        string
		served, failed map[config.KindRef]bool
	}
	perCluster := make([]clusterKinds, 0, len(results))
	union := make(map[config.KindRef]bool)
	for _, r := range results {
		served, failed := servedAndFailed(r)
		perCluster = append(perCluster, clusterKinds{r.snap.Cluster, served, failed})
		for kr := range served {
			union[kr] = true
		}
	}

	included := make(map[config.KindRef]bool, len(union))
	var excluded []excludedKind
	for kr := range union {
		reason := ""
		for _, ck := range perCluster {
			if !ck.served[kr] {
				reason = ck.cluster + ": not served"
				break
			}
			if ck.failed[kr] {
				reason = ck.cluster + ": listing failed"
				break
			}
		}
		if reason == "" {
			included[kr] = true
		} else {
			kind := kr.Kind
			if kr.Group != "" {
				kind = kr.Group + "/" + kr.Kind
			}
			excluded = append(excluded, excludedKind{kind: kind, reason: reason})
		}
	}
	sort.Slice(excluded, func(i, j int) bool { return excluded[i].kind < excluded[j].kind })
	return included, excluded
}

// carriedExceptions loads the exceptions block from an existing
// artifact at outPath so regeneration never silently clobbers hand-edited
// exceptions. A file that exists but fails to load is fatal.
func carriedExceptions(cmd *cobra.Command, outPath string) ([]baseline.ApprovedException, error) {
	if discard, _ := cmd.Flags().GetBool("discard-exceptions"); discard || outPath == "-" {
		return nil, nil
	}
	if _, err := os.Stat(outPath); err != nil {
		return nil, nil // nothing to carry
	}
	existing, err := baseline.Load(outPath)
	if err != nil {
		return nil, exitErrorf(1, "refusing to overwrite %s: %v (use --discard-exceptions to replace it deliberately)", outPath, err)
	}
	if len(existing.Exceptions) > 0 {
		fmt.Fprintf(os.Stderr, "baseline: kept %d exception(s) from %s\n", len(existing.Exceptions), outPath)
	}
	return existing.Exceptions, nil
}

// writeBaseline writes atomically (temp file + rename) so a failed run never
// truncates an existing artifact; "-" streams to stdout.
func writeBaseline(b *baseline.Baseline, outPath string) error {
	asJSON := strings.HasSuffix(outPath, ".json")
	if outPath == "-" {
		return b.Write(os.Stdout, asJSON)
	}
	tmp, err := os.CreateTemp(filepath.Dir(outPath), ".baseline-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after successful rename
	if err := b.Write(tmp, asJSON); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), outPath)
}

// snapshotsOf extracts the snapshots from scan results.
func snapshotsOf(results []clusterResult) []*inventory.Snapshot {
	snaps := make([]*inventory.Snapshot, 0, len(results))
	for _, r := range results {
		snaps = append(snaps, r.snap)
	}
	return snaps
}
