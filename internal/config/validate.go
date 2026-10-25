package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// namespaceNameRE is the Kubernetes namespace name rule (RFC 1123 label;
// length is checked separately). config may not import apimachinery.
var namespaceNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ParseKindRef parses the canonical "group/Kind" wire syntax (ADR-0003). No
// slash means core group. Splitting is on the FIRST slash only.
func ParseKindRef(s string) (KindRef, error) {
	if s == "" {
		return KindRef{}, fmt.Errorf("kind reference is empty")
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 1 {
		return KindRef{Kind: parts[0]}, nil
	}
	group, kind := parts[0], parts[1]
	if kind == "" {
		return KindRef{}, fmt.Errorf("kind reference %q has an empty kind", s)
	}
	if strings.Contains(kind, "/") {
		return KindRef{}, fmt.Errorf("kind reference %q has more than one slash; write group/Kind without the version, e.g. apps/Deployment", s)
	}
	return KindRef{Group: group, Kind: kind}, nil
}

// validateFieldPath does a lightweight syntactic check of a content.ignoreFields
// path. The full grammar (and its semantics) lives in internal/normalize, which
// config may not import (DAG); this catches the obvious mistakes — empty paths
// and unbalanced brackets — so misconfiguration fails at load, not at comparison time.
func validateFieldPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("path is empty")
	}
	depth := 0
	for _, c := range p {
		switch c {
		case '[':
			depth++
		case ']':
			depth--
			if depth < 0 {
				return fmt.Errorf("%q: unbalanced ']'", p)
			}
		}
	}
	if depth != 0 {
		return fmt.Errorf("%q: unbalanced '['", p)
	}
	return nil
}

// Validate checks the config for internally-inconsistent or out-of-range
// values. Every error names the offending key so misconfiguration is
// actionable without reading this file.
func (c *Config) Validate() error {
	var errs []string

	if c.Analysis.CommonThreshold <= 0 || c.Analysis.CommonThreshold > 1 {
		errs = append(errs, fmt.Sprintf("analysis.commonThreshold: must be in (0,1], got %v", c.Analysis.CommonThreshold))
	}
	if c.Analysis.RareThreshold <= 0 || c.Analysis.RareThreshold > 1 {
		errs = append(errs, fmt.Sprintf("analysis.rareThreshold: must be in (0,1], got %v", c.Analysis.RareThreshold))
	}
	if c.Analysis.RareThreshold >= c.Analysis.CommonThreshold {
		errs = append(errs, fmt.Sprintf("analysis.rareThreshold: must be less than analysis.commonThreshold (%v >= %v)", c.Analysis.RareThreshold, c.Analysis.CommonThreshold))
	}
	if c.Analysis.MinScore < 0 || c.Analysis.MinScore > 100 {
		errs = append(errs, fmt.Sprintf("--min-score / analysis.minScore: must be in [0,100], got %d", c.Analysis.MinScore))
	}
	if c.Clusters.ProbeTimeout <= 0 {
		errs = append(errs, fmt.Sprintf("--probe-timeout / clusters.probeTimeout: must be positive, got %v", c.Clusters.ProbeTimeout))
	}
	if c.Clusters.RequestTimeout <= 0 {
		errs = append(errs, fmt.Sprintf("--request-timeout / clusters.requestTimeout: must be positive, got %v", c.Clusters.RequestTimeout))
	}

	for _, ref := range c.Scope.Kinds.Exclude {
		if _, err := ParseKindRef(ref); err != nil {
			errs = append(errs, fmt.Sprintf("scope.kinds.exclude: %v", err))
		}
	}
	for _, ref := range c.Scope.Kinds.Include {
		if _, err := ParseKindRef(ref); err != nil {
			errs = append(errs, fmt.Sprintf("scope.kinds.include: %v", err))
		}
	}

	for _, ns := range c.Scope.Namespaces.Include {
		if len(ns) > 63 || !namespaceNameRE.MatchString(ns) {
			errs = append(errs, fmt.Sprintf("-n/--namespaces (scope.namespaces.include): %q is not a valid namespace name", ns))
		}
	}

	for _, pat := range c.Scope.Namespaces.ExcludePatterns {
		if _, err := regexp.Compile("^(?:" + pat + ")$"); err != nil {
			errs = append(errs, fmt.Sprintf("scope.namespaces.excludePatterns: %q: %v", pat, err))
		}
	}

	for _, ref := range c.Content.Kinds {
		if _, err := ParseKindRef(ref); err != nil {
			errs = append(errs, fmt.Sprintf("content.kinds: %v", err))
		}
	}
	for _, p := range c.Content.IgnoreFields {
		if err := validateFieldPath(p); err != nil {
			errs = append(errs, fmt.Sprintf("content.ignoreFields: %v", err))
		}
	}
	for _, ref := range c.Sensitive.Kinds {
		if _, err := ParseKindRef(ref); err != nil {
			errs = append(errs, fmt.Sprintf("sensitive.kinds: %v", err))
		}
	}

	switch c.Report.Format {
	case FormatTable, FormatJSON, FormatYAML, FormatMarkdown:
	default:
		errs = append(errs, fmt.Sprintf("-o/--output (report.format): invalid value %q; use table, json, yaml or markdown", c.Report.Format))
	}

	switch c.Report.FailOn {
	case FailOnNone, FailOnInventory, FailOnContent, FailOnAny:
	default:
		errs = append(errs, fmt.Sprintf("--fail-on (report.failOn): invalid value %q; use none, inventory, content or any", c.Report.FailOn))
	}

	if len(c.Clusters.Contexts) > 0 && c.Clusters.AllContexts {
		errs = append(errs, "--contexts and --all-contexts (clusters.contexts, clusters.allContexts): mutually exclusive, set only one")
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
