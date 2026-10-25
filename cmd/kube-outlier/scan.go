package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/vibe-shift-coding/kube-outlier/internal/analyze"
	"github.com/vibe-shift-coding/kube-outlier/internal/baseline"
	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/connect"
	"github.com/vibe-shift-coding/kube-outlier/internal/discovery"
	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
	"github.com/vibe-shift-coding/kube-outlier/internal/inventory"
	"github.com/vibe-shift-coding/kube-outlier/internal/report"
)

// scanClusterConcurrency bounds how many clusters are scanned in parallel.
const scanClusterConcurrency = 4

func newScanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for resources that were probably created by hand and never cleaned up",
		Long: `Scan lists every resource type in scope (metadata only) and scores each
resource with the ownership signal: pipeline markers, managedFields evidence
of a kubectl client, kind and age. With two or more clusters, or --baseline,
the fleet presence signal adjusts those scores and reports resources missing
from a cluster. --compare-content also compares what shared resources contain.

The report goes to stdout; warnings, skipped clusters or types, and the
table's summary line go to stderr.

Exit codes: 0 clean; 1 fatal; 2 partial run (something was skipped);
3 inventory findings and 4 content differences, with --fail-on.`,
		Example: `  # Scan the current context
  kube-outlier scan

  # Only some namespaces (also what namespace-only RBAC needs)
  kube-outlier scan -n team-a,team-b

  # A fleet, with the content comparison
  kube-outlier scan --contexts cluster-eu1,cluster-us1,cluster-ap1 --compare-content

  # Why was this reported, or not?
  kube-outlier scan --why "ConfigMap team-a/debug-dump"

  # CI: JSON, and exit 3 when something turns up
  kube-outlier scan --baseline baseline.yaml --fail-on inventory -o json > findings.json`,
		Args: cobra.NoArgs,
		RunE: runScan,
	}
	addFleetFlags(cmd)
	addOutputFlag(cmd)
	cmd.Flags().StringSliceP("namespaces", "n", nil, "scan only these namespaces, comma-separated or repeated (alias --namespace); works with namespace-scoped RBAC")
	cmd.Flags().Bool("include-cluster-scoped", false, "also scan cluster-scoped kinds (ClusterRoleBindings, CRDs, ...)")
	cmd.Flags().Int("min-score", 40, "hide findings scoring below this (0-100; analysis.minScore)")
	cmd.Flags().String("fail-on", "none", "exit non-zero when findings are reported: none|inventory|content|any (report.failOn). inventory (exit 3) = likely-manual and missing-from-cluster; content (exit 4) = content-differs, needs the content comparison to run")
	cmd.Flags().Bool("allow-partial", false, "exit 0 instead of 2 when a cluster, resource type or (with --compare-content) object was skipped and nothing else triggered (skips are still reported)")
	cmd.Flags().String("baseline", "", "compare each cluster against this baseline file instead of live peers")
	cmd.Flags().Bool("allow-scope-mismatch", false, "warn instead of failing when the baseline was made with a different scan scope")
	cmd.Flags().String("why", "", `show every piece of evidence for one resource instead of the report, e.g. "ConfigMap default/debug-dump" (text output only)`)
	cmd.Flags().Bool("compare-content", false, "also compare what shared resources contain across clusters, field by field, for content.kinds (reads full objects; needs 2+ reachable clusters)")
	return cmd
}

func runScan(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	whyArg, _ := cmd.Flags().GetString("why")
	allowMismatch, _ := cmd.Flags().GetBool("allow-scope-mismatch")
	compareContent, _ := cmd.Flags().GetBool("compare-content")
	compareContent = compareContent || cfg.Content.Enabled
	baselinePath, _ := cmd.Flags().GetString("baseline")

	var whyKey identity.ResourceKey
	if whyArg != "" {
		if whyKey, err = parseWhyKey(whyArg); err != nil {
			return exitErrorf(1, "%v", err)
		}
		// --why renders text. An explicit -o for another format is a
		// mistake; report.format from a config file or the environment is
		// meant for reports and does not apply here.
		if cmd.Flags().Changed("output") && cfg.Report.Format != config.FormatTable {
			return exitErrorf(1, "--why output is text only; drop -o %s", cfg.Report.Format)
		}
		if cmd.Flags().Changed("fail-on") {
			fmt.Fprintln(os.Stderr, "warning: --fail-on has no effect with --why")
		}
	} else if !compareContent {
		// Without --compare-content no content-differs findings exist, so a content trigger could
		// never fire and a CI gate would pass silently.
		switch cfg.Report.FailOn {
		case config.FailOnContent:
			return exitErrorf(1, "--fail-on content (report.failOn) needs --compare-content or content.enabled: true; without it content is never compared")
		case config.FailOnAny:
			fmt.Fprintln(os.Stderr, "warning: --fail-on any checks inventory findings only without --compare-content; add --compare-content to also fail on content differences")
		}
	}

	now := time.Now()

	refs, results, err := collectFleet(cmd.Context(), cfg)
	if err != nil {
		return err
	}
	warnEmptyNamespaces(os.Stderr, results, cfg)
	warnUnmatchedKinds(os.Stderr, results, cfg)

	snaps := snapshotsOf(results)

	// Fleet presence source: a --baseline file replaces live peers entirely
	// (each cluster compared against it independently); otherwise Tier 2
	// activates when two or more clusters produced snapshots; a single
	// contributing cluster runs the plain Tier 1 path (presence data is never
	// fabricated).
	var findings []analyze.Finding
	var suppressed, suppressedByException int
	var base *loadedBaseline
	presence := report.FleetPresence{Applied: false, Source: "none"}
	if baselinePath != "" {
		if base, err = loadBaselineContext(baselinePath, allowMismatch, cfg); err != nil {
			return err
		}
		findings, suppressed, suppressedByException = baselineScan(os.Stderr, base, results, cfg, now)
		presence = report.FleetPresence{Applied: true, Source: "baseline"}
	} else if len(snaps) >= 2 {
		findings, suppressed = analyze.Tier2Findings(snaps, fleetInputs(results), cfg.Analysis, now)
		presence = report.FleetPresence{Applied: true, Source: "live"}
	} else {
		for _, snap := range snaps {
			f, s := analyze.Tier1Findings(snap, cfg.Analysis, now)
			findings = append(findings, f...)
			suppressed += s
		}
	}

	var unread []report.SkippedObject
	if compareContent {
		content, err := runContentComparison(cmd.Context(), results, cfg, now)
		if err != nil {
			return err
		}
		if content.skipReason != "" && whyArg == "" {
			switch cfg.Report.FailOn {
			case config.FailOnContent:
				return exitErrorf(1, "--fail-on content: the content comparison could not run (%s), so the gate could never fail", content.skipReason)
			case config.FailOnAny:
				fmt.Fprintf(os.Stderr, "warning: --fail-on any checks inventory findings only: the content comparison did not run (%s)\n", content.skipReason)
			}
		}
		findings = append(findings, content.findings...)
		unread = content.unread
	}

	if whyArg != "" {
		return runWhy(cmd, whyKey, refs, results, cfg, findings, len(unread) > 0, base, presence.Source, now)
	}

	rep := report.Build(refs, cfg.Scope, snaps, findings, suppressed, presence, version(), now)
	rep.Summary.SuppressedByException = suppressedByException
	rep.SkippedObjects = unread
	rep.Partial = report.IsPartial(rep)
	rep.Listings = listingCounts(results, cfg)
	rep.ExitCode = report.ExitCode(rep, cfg.Report)
	slog.Info("scan finished", "findings", rep.Summary.Total, "belowMinScore", suppressed, "exitCode", rep.ExitCode, "took", time.Since(now).Round(time.Millisecond))

	for _, line := range report.SkipSummaryLines(rep) {
		fmt.Fprintln(os.Stderr, line)
	}

	out := cmd.OutOrStdout()
	var renderErr error
	switch cfg.Report.Format {
	case config.FormatJSON:
		renderErr = report.RenderJSON(out, rep)
	case config.FormatYAML:
		renderErr = report.RenderYAML(out, rep)
	case config.FormatMarkdown:
		renderErr = report.RenderMarkdown(out, rep)
	default:
		renderErr = report.RenderTableWidth(out, rep, terminalWidth(out))
		if renderErr == nil {
			switch {
			case len(rep.Findings) > 0:
				if rep.Summary.ByClass[string(analyze.ClassContentDiffers)] > 0 {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr()) // set off from the content differences
				}
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), report.SummaryLine(rep))
			case report.NothingRead(rep):
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "No findings — nothing could be read (see the skipped types above).")
			default:
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "No findings.")
			}
		}
	}
	if renderErr != nil {
		return exitErrorf(1, "rendering report: %v", renderErr)
	}

	return exitSilently(rep.ExitCode)
}

// runWhy replaces the findings report with the detail view for one
// identity. findings are the ones the report would have shown; unread says
// the content comparison could not read some copies. failOn never applies:
// exit 0, or 2 for a partial run unless report.allowPartial is set.
func runWhy(cmd *cobra.Command, key identity.ResourceKey, refs []connect.ClusterRef, results []clusterResult, cfg *config.Config, findings []analyze.Finding, unread bool, base *loadedBaseline, source string, now time.Time) error {
	in := whyInputs{results: results, cfg: cfg, source: source, reported: findings, now: now}
	if base != nil {
		in.bref = &base.ref
		in.active, _ = base.b.ActiveExceptions(now)
	}

	e, err := buildWhy(key, in)
	if err != nil {
		return exitErrorf(1, "%v", err)
	}
	if err := report.RenderExplain(cmd.OutOrStdout(), e); err != nil {
		return exitErrorf(1, "rendering --why: %v", err)
	}

	partial := unread
	for _, r := range refs {
		if !r.Reachable {
			partial = true
		}
	}
	for _, r := range results {
		if len(r.snap.Errors) > 0 {
			partial = true
		}
	}
	if partial && !cfg.Report.AllowPartial {
		return exitSilently(2)
	}
	return nil
}

// collectFleet resolves, probes and scans the requested clusters, failing
// (exit 1) only when none can be used. A variable so tests can run the
// commands without a cluster.
var collectFleet = func(ctx context.Context, cfg *config.Config) ([]connect.ClusterRef, []clusterResult, error) {
	refs, err := connect.Resolve(cfg)
	if err != nil {
		return nil, nil, exitErrorf(1, "%v", err)
	}
	refs = connect.ProbeAll(ctx, refs, cfg.Clusters.ProbeTimeout)

	reachable := 0
	for _, r := range refs {
		if r.Reachable {
			reachable++
		}
	}
	if reachable == 0 {
		return nil, nil, noReachableClusters("no reachable clusters", refs)
	}
	return refs, scanAllClusters(ctx, refs, cfg), nil
}

// warnEmptyNamespaces says on stderr when a namespace named with -n held
// nothing at all on a cluster that could list it. Every namespace has a
// default ServiceAccount and a kube-root-ca.crt ConfigMap, so an empty one
// almost certainly does not exist there (often a typo). Namespaces the
// identity could not list are already reported as skipped.
func warnEmptyNamespaces(w io.Writer, results []clusterResult, cfg *config.Config) {
	if len(cfg.Scope.Namespaces.Include) == 0 {
		return
	}
	for _, r := range results {
		seen := map[string]bool{}
		for _, res := range r.snap.Resources {
			seen[res.Key.Namespace] = true
		}
		for _, e := range r.snap.Errors {
			seen[e.Namespace] = true
		}
		for _, ns := range cfg.Scope.Namespaces.Include {
			if !seen[ns] {
				_, _ = fmt.Fprintf(w, "warning: %s: nothing found in namespace %s; does it exist?\n", r.snap.Cluster, ns)
			}
		}
	}
}

// clusterResult is one successfully scanned cluster: its snapshot, the
// discovery output that produced it (needed to build analyze.ServedKinds),
// and the client bundle that scanned it, reused for Pass B so each cluster
// is connected to (and discovered) once. clients is nil in tests. served is
// every type the cluster serves, before the scope filters, so a kind in the
// config that matches nothing can be told from one the scope leaves out.
type clusterResult struct {
	snap    *inventory.Snapshot
	gvrs    []discovery.GVRInfo
	served  []discovery.GVRInfo
	clients *connect.Clients
}

// scanAllClusters runs discovery -> inventory for every reachable cluster in
// refs, bounded to scanClusterConcurrency at a time, and returns one result
// per cluster that produced a snapshot. A cluster whose client construction,
// discovery, or collection fails entirely is flipped to Reachable=false with
// SkipReason set (behaves like an unreachable cluster for reporting purposes)
// rather than aborting the whole run.
func scanAllClusters(ctx context.Context, refs []connect.ClusterRef, cfg *config.Config) []clusterResult {
	results := make([]clusterResult, len(refs))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(scanClusterConcurrency)
	for i := range refs {
		if !refs[i].Reachable {
			continue
		}
		g.Go(func() error {
			results[i] = scanOneCluster(gctx, &refs[i], cfg)
			return nil
		})
	}
	_ = g.Wait()

	out := make([]clusterResult, 0, len(results))
	for _, r := range results {
		if r.snap != nil {
			out = append(out, r)
		}
	}
	return out
}

// scanOneCluster returns a zero clusterResult if client construction,
// discovery, or inventory collection fails entirely for this cluster, having
// first marked ref as skipped (fail-soft: the run continues without it). An
// API group whose discovery failed is recorded as a skipped type, so the run
// is partial and the group's kinds never count as absent there.
func scanOneCluster(ctx context.Context, ref *connect.ClusterRef, cfg *config.Config) clusterResult {
	skip := func(err error) clusterResult {
		ref.Reachable = false
		ref.SkipReason = err.Error()
		return clusterResult{}
	}

	start := time.Now()
	clients, err := connect.New(*ref, cfg.Clusters.RequestTimeout)
	if err != nil {
		return skip(err)
	}

	disc, err := discovery.ListScannable(ctx, clients.Discovery, cfg.Scope)
	if err != nil {
		return skip(err)
	}
	gvrs := disc.GVRs

	snap, err := inventory.CollectMetadata(ctx, ref.Name, clients.Metadata, gvrs, cfg.Scope, inventory.Options{})
	if err != nil {
		return skip(err)
	}
	for _, fg := range disc.Failed {
		snap.Errors = append(snap.Errors, inventory.GVRError{
			GVR: fg.GroupVersion.WithResource("*"),
			Err: "discovery failed: " + fg.Err,
		})
	}
	slog.Info("scanned cluster", "cluster", ref.Name, "types", len(gvrs), "resources", len(snap.Resources), "skippedTypes", len(snap.Errors), "took", time.Since(start).Round(time.Millisecond))
	return clusterResult{snap: snap, gvrs: gvrs, served: disc.Served, clients: clients}
}

// servedAndFailed derives one cluster's kind sets from its scan result: the
// {Group, Kind} its discovery served, and the subset whose GVR could not
// actually be listed there (mapped back to Kinds via the GVRInfo list — the
// reason this lives in cmd, not analyze).
func servedAndFailed(r clusterResult) (served, failed map[config.KindRef]bool) {
	kindByGVR := make(map[schema.GroupVersionResource]string, len(r.gvrs))
	served = make(map[config.KindRef]bool, len(r.gvrs))
	for _, gi := range r.gvrs {
		kindByGVR[gi.GVR] = gi.Kind
		served[config.KindRef{Group: gi.GVR.Group, Kind: gi.Kind}] = true
	}
	failed = make(map[config.KindRef]bool, len(r.snap.Errors))
	for _, e := range r.snap.Errors {
		if kind, ok := kindByGVR[e.GVR]; ok {
			failed[config.KindRef{Group: e.GVR.Group, Kind: kind}] = true
		}
	}
	return served, failed
}

// fleetInputs derives the Tier 2 inputs from the per-cluster scan results:
// the presence matrix plus every cluster's served/failed kind sets (analyze
// subtracts failed kinds from presence denominators).
func fleetInputs(results []clusterResult) analyze.FleetInputs {
	snaps := make([]*inventory.Snapshot, 0, len(results))
	served := analyze.ServedKinds{}
	failed := analyze.ServedKinds{}
	for _, r := range results {
		snaps = append(snaps, r.snap)
		served[r.snap.Cluster], failed[r.snap.Cluster] = servedAndFailed(r)
	}
	return analyze.FleetInputs{
		Matrix: analyze.BuildPresence(snaps),
		Served: served,
		Failed: failed,
	}
}

// loadedBaseline is a validated baseline and the analyze handoff built from it.
type loadedBaseline struct {
	b   *baseline.Baseline
	ref analyze.BaselineRef
}

// loadBaselineContext loads + validates the artifact, enforces the scope
// fingerprint (fatal on mismatch unless allowMismatch, ADR-0004), and builds
// the analyze.BaselineRef handoff.
func loadBaselineContext(path string, allowMismatch bool, cfg *config.Config) (*loadedBaseline, error) {
	b, err := baseline.Load(path)
	if err != nil {
		return nil, exitErrorf(1, "%v", err)
	}

	current := baseline.FingerprintScope(cfg.Scope)
	if current.Hash != b.Provenance.Scope.Hash {
		fmt.Fprintf(os.Stderr, "baseline %s was made with a different scan scope, so its presence counts are not comparable with this scan (+ only in this scan, - only in the baseline):\n", path)
		for _, line := range baseline.Diff(b.Provenance.Scope, current) {
			fmt.Fprintln(os.Stderr, "  "+line)
		}
		if !allowMismatch {
			return nil, exitErrorf(1, "baseline scope mismatch: regenerate the baseline with the same scope (baseline generate takes -n and --include-cluster-scoped, and reads the same config), or pass --allow-scope-mismatch to proceed anyway")
		}
		fmt.Fprintln(os.Stderr, "warning: proceeding despite scope mismatch (--allow-scope-mismatch)")
	}

	return &loadedBaseline{b: b, ref: baselineRefOf(b)}, nil
}

// baselineRefOf distills a loaded baseline into the analyze handoff.
func baselineRefOf(b *baseline.Baseline) analyze.BaselineRef {
	ref := analyze.BaselineRef{
		FleetSize: b.Provenance.ClusterCount,
		Clusters:  b.Provenance.Clusters,
		Entries:   make(map[identity.ResourceKey]analyze.BaselineEntryRef, len(b.Entries)),
		Kinds:     make(map[config.KindRef]bool),
	}
	for _, e := range b.Entries {
		ref.Entries[e.Key.ResourceKey()] = analyze.BaselineEntryRef{ClusterCount: e.ClusterCount, Ratio: e.Ratio}
		ref.Kinds[config.KindRef{Group: e.Key.Group, Kind: e.Key.Kind}] = true
	}
	return ref
}

// baselineScan compares every snapshotted cluster independently against the
// loaded baseline, then drops findings matching active approved exceptions.
// Returns findings, the below-minScore count, and the suppressed-by-exception
// count. Warnings (expired exceptions, exceptions that name nothing) go to w.
func baselineScan(w io.Writer, base *loadedBaseline, results []clusterResult, cfg *config.Config, now time.Time) ([]analyze.Finding, int, int) {
	var findings []analyze.Finding
	suppressed := 0
	for _, r := range results {
		served, failed := servedAndFailed(r)
		f, s := analyze.BaselineFindings(r.snap, base.ref, served, failed, cfg.Analysis, now)
		findings = append(findings, f...)
		suppressed += s
	}

	active, expired := base.b.ActiveExceptions(now)
	for _, e := range expired {
		_, _ = fmt.Fprintf(w, "baseline: exception for %s expired %s, ignoring\n",
			e.Key.String(), e.Expires.UTC().Format("2006-01-02"))
	}
	warnUnknownExceptions(w, base, active, results)
	byException := 0
	kept := findings[:0]
	for _, f := range findings {
		if _, ok := active[f.Key]; ok {
			byException++
			continue
		}
		kept = append(kept, f)
	}
	return kept, suppressed, byException
}
