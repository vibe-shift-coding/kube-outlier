package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/connect"
	"github.com/vibe-shift-coding/kube-outlier/internal/orderedyaml"
)

func newClustersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clusters",
		Short: "Check which clusters are reachable, and their Kubernetes versions",
		Long: `Clusters resolves the requested kubeconfig contexts, probes each one, and lists
it with its server version, or the reason it could not be reached. It needs
only the discovery API (/version), which every authenticated user has.

A cluster's name in every report is its context name.

Exit codes: 0 all reachable; 2 some unreachable; 1 none reachable or bad flags.`,
		Example: `  # The current context
  kube-outlier clusters

  # Several contexts, or every context in the kubeconfig
  kube-outlier clusters --contexts cluster-eu1,cluster-us1
  kube-outlier clusters --all-contexts

  # One kubeconfig file per cluster
  kube-outlier clusters --kubeconfig eu.yaml --kubeconfig us.yaml --all-contexts`,
		Args: cobra.NoArgs,
		RunE: runClusters,
	}
	addFleetFlags(cmd)
	addOutputFlag(cmd)
	return cmd
}

func runClusters(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	refs, err := connect.Resolve(cfg)
	if err != nil {
		return exitErrorf(1, "%v", err)
	}

	probed := connect.ProbeAll(cmd.Context(), refs, cfg.Clusters.ProbeTimeout)

	reachable := 0
	for _, r := range probed {
		if r.Reachable {
			reachable++
		}
	}
	if reachable == 0 {
		return noReachableClusters("no reachable clusters", probed)
	}

	out := cmd.OutOrStdout()
	switch cfg.Report.Format {
	case config.FormatJSON:
		if err := renderClustersJSON(out, probed, len(refs), reachable, false); err != nil {
			return exitErrorf(1, "rendering json: %v", err)
		}
	case config.FormatYAML:
		if err := renderClustersJSON(out, probed, len(refs), reachable, true); err != nil {
			return exitErrorf(1, "rendering yaml: %v", err)
		}
	case config.FormatMarkdown:
		renderClustersMarkdown(out, probed)
	default:
		renderClustersTable(out, probed)
	}

	if reachable < len(probed) {
		return exitSilently(2)
	}
	return nil
}

// renderClustersTable writes one row per cluster. The cluster name is the
// context name, so there is no separate CONTEXT column (JSON keeps both).
// Lines are right-trimmed: tabwriter pads before an empty NOTE cell.
func renderClustersTable(w io.Writer, refs []connect.ClusterRef) {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CLUSTER\tREACHABLE\tVERSION\tNOTE")
	for _, r := range refs {
		reachable := "no"
		version := "-"
		note := r.SkipReason
		if r.Reachable {
			reachable = "yes"
			version = r.ServerVersion
			note = ""
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, reachable, version, note)
	}
	_ = tw.Flush()
	for _, line := range strings.SplitAfter(buf.String(), "\n") {
		if line == "" {
			continue
		}
		_, _ = io.WriteString(w, strings.TrimRight(line, " \n")+"\n")
	}
}

// fleetClusterJSON, fleetJSON and clustersOutputJSON are the `clusters -o
// json` envelope, frozen in interfaces.md §3c. Its fleet block is the same
// shape as the scan report's.
type fleetClusterJSON struct {
	Name          string `json:"name"`
	Context       string `json:"context"`
	Reachable     bool   `json:"reachable"`
	ServerVersion string `json:"serverVersion,omitempty"`
	SkipReason    string `json:"skipReason,omitempty"`
}

type fleetJSON struct {
	Requested int                `json:"requested"`
	Reachable int                `json:"reachable"`
	Clusters  []fleetClusterJSON `json:"clusters"`
}

type clustersOutputJSON struct {
	SchemaVersion int       `json:"schemaVersion"`
	ToolVersion   string    `json:"toolVersion"`
	GeneratedAt   string    `json:"generatedAt"`
	Fleet         fleetJSON `json:"fleet"`
}

func renderClustersJSON(w io.Writer, refs []connect.ClusterRef, requested, reachable int, asYAML bool) error {
	clusters := make([]fleetClusterJSON, 0, len(refs))
	for _, r := range refs {
		clusters = append(clusters, fleetClusterJSON{
			Name:          r.Name,
			Context:       r.Context,
			Reachable:     r.Reachable,
			ServerVersion: r.ServerVersion,
			SkipReason:    r.SkipReason,
		})
	}
	out := clustersOutputJSON{
		SchemaVersion: 1,
		ToolVersion:   version(),
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Fleet: fleetJSON{
			Requested: requested,
			Reachable: reachable,
			Clusters:  clusters,
		},
	}
	if asYAML { // same envelope, YAML syntax
		raw, err := orderedyaml.Marshal(out)
		if err != nil {
			return err
		}
		_, err = w.Write(raw)
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false) // integration surface, not HTML; keep <, >, & verbatim
	return enc.Encode(out)
}

// renderClustersMarkdown mirrors the table columns as a GitHub table.
func renderClustersMarkdown(w io.Writer, refs []connect.ClusterRef) {
	_, _ = fmt.Fprintf(w, "## kube-outlier clusters\n\n")
	_, _ = fmt.Fprintln(w, "| CLUSTER | REACHABLE | VERSION | NOTE |")
	_, _ = fmt.Fprintln(w, "| --- | --- | --- | --- |")
	for _, r := range refs {
		reachable, version, note := "no", "-", r.SkipReason
		if r.Reachable {
			reachable, version, note = "yes", r.ServerVersion, ""
		}
		_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s |\n", r.Name, reachable, version, strings.ReplaceAll(note, "|", "\\|"))
	}
}
