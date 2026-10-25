package main

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// toolVersion is overridable via -ldflags "-X main.toolVersion=vX.Y.Z" at
// release build time; "dev" otherwise. It must stay a plain string literal
// for -X to work. Use version() for the effective value.
var toolVersion = "dev"

// version is the effective tool version recorded in reports and
// baselines: toolVersion when set at build time, else the module version
// that `go install …@vX.Y.Z` stamps into the binary.
func version() string {
	return resolveVersion(toolVersion, debug.ReadBuildInfo)
}

// resolveVersion returns ldflagsVersion unless it is the "dev" default, in
// which case it falls back to the main module version from the build info.
// Builds without module or VCS version information report "(devel)", which
// keeps "dev"; a build inside a git checkout gets a VCS pseudo-version.
func resolveVersion(ldflagsVersion string, buildInfo func() (*debug.BuildInfo, bool)) string {
	if ldflagsVersion != "dev" {
		return ldflagsVersion
	}
	if info, ok := buildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return ldflagsVersion
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the kube-outlier version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "kube-outlier %s\n", version())
			return err
		},
	}
}
