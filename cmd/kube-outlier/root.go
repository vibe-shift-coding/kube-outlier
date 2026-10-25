package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
	"github.com/vibe-shift-coding/kube-outlier/internal/connect"
)

// exitError carries the process exit code alongside an optional message.
// A nil msg means "already communicated to the user" (e.g. via a rendered
// table) — Execute won't print anything extra for it.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func exitErrorf(code int, format string, args ...any) error {
	return &exitError{code: code, msg: fmt.Sprintf(format, args...)}
}

func exitSilently(code int) error {
	return &exitError{code: code}
}

// noReachableClusters is the fatal (exit 1) error for a run in which no
// cluster could be used. headline is the first line of the message; one
// indented line per cluster follows with the reason it was skipped, so the
// user sees why without re-running with -v.
func noReachableClusters(headline string, refs []connect.ClusterRef) error {
	var b strings.Builder
	b.WriteString(headline)
	for _, r := range refs {
		reason := r.SkipReason
		if reason == "" {
			reason = "skipped"
		}
		fmt.Fprintf(&b, "\n  %s: %s", r.Name, reason)
	}
	return &exitError{code: 1, msg: b.String()}
}

// Execute runs the CLI and returns the process exit code. Ctrl-C or SIGTERM
// cancels in-flight API calls instead of leaving them to time out.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCmd()
	err := root.ExecuteContext(ctx)
	code, msg := exitCodeAndMessage(err)
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	return code
}

// exitCodeAndMessage translates a command's returned error into a process
// exit code and the message (if any) to print to stderr.
func exitCodeAndMessage(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code, ee.msg
	}
	return 1, err.Error()
}

// flagAliases maps kubectl's singular spellings onto ours, so `--context c1`
// and `--namespace ns` work as kubectl users expect. Help shows only the
// canonical names.
var flagAliases = map[string]string{
	"context":   "contexts",
	"namespace": "namespaces",
}

func normalizeFlagName(_ *pflag.FlagSet, name string) pflag.NormalizedName {
	if canonical, ok := flagAliases[name]; ok {
		name = canonical
	}
	return pflag.NormalizedName(name)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "kube-outlier",
		Short: "Find Kubernetes resources that were probably created by hand and never cleaned up",
		Long: `kube-outlier is a read-only CLI that finds Kubernetes resources a human created
by hand and never cleaned up: the ConfigMap someone kubectl apply'd during an
incident, the debug NetworkPolicy from last quarter.

It scores each resource with two signals — ownership (pipeline markers and
managedFields) and, with two or more clusters or a baseline, fleet presence
(which clusters have it) — and can optionally compare what shared resources
contain across clusters. Findings describe what is unusual, never what is wrong.

It uses your kubeconfig, needs only list (and get for --compare-content), and
never writes to a cluster.`,
		Example: `  # Which clusters can I reach?
  kube-outlier clusters --contexts cluster-eu1,cluster-us1

  # Scan the current context
  kube-outlier scan

  # Scan a fleet; the fleet presence signal turns on at 2+ clusters
  kube-outlier scan --contexts cluster-eu1,cluster-us1,cluster-ap1

  # See why something was (or wasn't) reported
  kube-outlier scan --why "ConfigMap team-a/debug-dump"`,
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version(),
	}
	// Same output as the version subcommand, so scripts can use either.
	root.SetVersionTemplate("kube-outlier {{.Version}}\n")
	root.SetGlobalNormalizationFunc(normalizeFlagName)
	// Usage is silenced (it would bury the error), so say where it is.
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return exitErrorf(1, "%v\nRun '%s --help' for usage.", err, cmd.CommandPath())
	})
	// Cobra's generated completion command stays available but undocumented.
	root.CompletionOptions.HiddenDefaultCmd = true

	root.PersistentFlags().CountP("verbose", "v", "log progress to stderr: -v for steps and timings, -vv for detail")

	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		verbosity, _ := cmd.Flags().GetCount("verbose")
		setupLogging(verbosity)
	}

	root.AddCommand(newClustersCmd())
	root.AddCommand(newScanCmd())
	root.AddCommand(newBaselineCmd())
	root.AddCommand(newVersionCmd())
	return root
}

// addFleetFlags registers the config file and cluster selection flags on the
// commands that load a config and connect to clusters (clusters, scan,
// baseline generate), so version and help don't list them.
func addFleetFlags(cmd *cobra.Command) {
	cmd.Flags().String("config", "", "path to config file (default: ./.kube-outlier.yaml, then $XDG_CONFIG_HOME/kube-outlier/config.yaml)")
	cmd.Flags().StringSlice("contexts", nil, "kubeconfig contexts to use, comma-separated or repeated (alias --context; default: the current context)")
	cmd.Flags().Bool("all-contexts", false, "use every context in the merged kubeconfig")
	cmd.Flags().StringArray("kubeconfig", nil, "kubeconfig file to load (repeatable; merged as with KUBECONFIG=a:b, first file wins on a clashing context name)")
	cmd.Flags().Duration("probe-timeout", 5*time.Second, "how long to wait for each cluster's reachability check")
	cmd.Flags().Duration("request-timeout", 30*time.Second, "upper bound on every other API request (discovery, list pages, gets)")
}

// addOutputFlag registers -o/--output on the commands that render a report.
// It is not global: baseline generate writes a file (--out) and version
// prints one line, so -o would be silently ignored there.
func addOutputFlag(cmd *cobra.Command) {
	cmd.Flags().StringP("output", "o", "table", "output format: table|json|yaml|markdown")
}

// loadConfig layers defaults, config file, environment and flags, validates
// the result, and says on stderr which config file was picked up when one
// was found in a default location (an explicit --config needs no reminder).
func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	configFile, _ := cmd.Flags().GetString("config")
	if configFile == "" {
		if found := config.DefaultConfigPath(); found != "" {
			fmt.Fprintf(os.Stderr, "using config file %s\n", found)
		}
	}
	cfg, err := config.Load(config.LoadOptions{File: configFile, Flags: cmd.Flags()})
	if err != nil {
		return nil, exitErrorf(1, "%v", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, exitErrorf(1, "%v", err)
	}
	return cfg, nil
}

// setupLogging sends logs to stderr. By default only warnings appear, in the
// same "warning: ..." form as the CLI's other messages; -v adds progress and
// timings and -vv detail, as structured lines. client-go logs through klog,
// which is routed here too, so its messages follow the same verbosity.
func setupLogging(verbosity int) {
	var handler slog.Handler
	switch {
	case verbosity >= 2:
		handler = quietHandler{slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})}
	case verbosity == 1:
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	default:
		handler = warningHandler{w: os.Stderr}
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)
	klog.SetSlogLogger(logger)
}

// warningHandler prints warnings and errors as "warning: <message> key=value"
// (or "error: ..."), matching the CLI's own stderr messages, and drops
// everything below warning level.
type warningHandler struct {
	w     io.Writer
	attrs []slog.Attr
}

// Enabled implements slog.Handler.
func (h warningHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }

// Handle implements slog.Handler.
func (h warningHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	if r.Level >= slog.LevelError {
		b.WriteString("error: ")
	} else {
		b.WriteString("warning: ")
	}
	b.WriteString(r.Message)
	writeAttr := func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	}
	for _, a := range h.attrs {
		writeAttr(a)
	}
	r.Attrs(writeAttr)
	b.WriteByte('\n')
	_, err := io.WriteString(h.w, b.String())
	return err
}

// WithAttrs implements slog.Handler.
func (h warningHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return h
}

// WithGroup implements slog.Handler. Groups are flattened: the warnings are
// for people, not log pipelines.
func (h warningHandler) WithGroup(string) slog.Handler { return h }

// noiseMessages are client-go log lines that say nothing about the scan;
// -vv drops them so its detail stays readable.
var noiseMessages = map[string]bool{
	"Feature gate default state": true,
}

// quietHandler drops noiseMessages and passes everything else on.
type quietHandler struct{ slog.Handler }

// Handle implements slog.Handler.
func (h quietHandler) Handle(ctx context.Context, r slog.Record) error {
	if noiseMessages[r.Message] {
		return nil
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h quietHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return quietHandler{h.Handler.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (h quietHandler) WithGroup(name string) slog.Handler {
	return quietHandler{h.Handler.WithGroup(name)}
}
