package connect

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/vibe-shift-coding/kube-outlier/internal/config"
)

// Resolve determines the requested fleet: which kubeconfig context(s), from
// which kubeconfig file(s). Selection is cfg.Clusters.Contexts, or
// AllContexts (sorted by name), or (if neither) the current-context alone.
// Returned refs have Reachable=false; probing is a separate step
// (Probe/ProbeAll). The files merge as kubectl merges KUBECONFIG=a:b: a
// context name defined in several files resolves to the first, and a context
// may use a user or cluster from another file. Files named explicitly
// (--kubeconfig, clusters.kubeconfigs) must be readable; a leading "~/" in
// them means the home directory.
func Resolve(cfg *config.Config) ([]ClusterRef, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	explicit := len(cfg.Clusters.Kubeconfigs) > 0
	if explicit {
		paths := make([]string, 0, len(cfg.Clusters.Kubeconfigs))
		for _, p := range cfg.Clusters.Kubeconfigs {
			expanded, err := expandHome(p)
			if err != nil {
				return nil, err
			}
			paths = append(paths, expanded)
		}
		rules.Precedence = paths
	}

	sourceFile := make(map[string]string) // context name -> kubeconfig file it was first found in
	var order []string                    // context names in discovery order, stable
	shadowed := make(map[string][]string) // context name -> later files that also define it

	checked := make([]string, 0, len(rules.Precedence)) // for the no-contexts error
	for _, file := range rules.Precedence {
		apiCfg, err := clientcmd.LoadFromFile(file)
		if err != nil {
			if explicit {
				return nil, fmt.Errorf("kubeconfig %s: %w", file, err)
			}
			// Tolerated, as clientcmd tolerates missing/unreadable files in the
			// KUBECONFIG search path, but named if nothing else is found.
			if errors.Is(err, fs.ErrNotExist) {
				checked = append(checked, file+" (not found)")
			} else {
				checked = append(checked, fmt.Sprintf("%s (unreadable: %v)", file, err))
			}
			continue
		}
		checked = append(checked, file)
		names := make([]string, 0, len(apiCfg.Contexts))
		for name := range apiCfg.Contexts {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if _, exists := sourceFile[name]; exists {
				// First file wins, exactly as kubectl merges KUBECONFIG=a:b.
				shadowed[name] = append(shadowed[name], file)
				continue
			}
			sourceFile[name] = file
			order = append(order, name)
		}
	}

	if len(order) == 0 {
		return nil, fmt.Errorf("no contexts found in any kubeconfig (checked: %s)", strings.Join(checked, ", "))
	}

	var selected []string
	switch {
	case cfg.Clusters.AllContexts:
		selected = append([]string(nil), order...)
		sort.Strings(selected)
	case len(cfg.Clusters.Contexts) > 0:
		requested := make(map[string]bool, len(cfg.Clusters.Contexts))
		for _, name := range cfg.Clusters.Contexts {
			if _, ok := sourceFile[name]; !ok {
				return nil, fmt.Errorf("unknown context %q (available: %s)", name, strings.Join(order, ", "))
			}
			if requested[name] {
				return nil, fmt.Errorf("context %q is listed more than once in --contexts", name)
			}
			requested[name] = true
			selected = append(selected, name)
		}
	default:
		merged, err := rules.Load()
		if err != nil {
			return nil, fmt.Errorf("loading kubeconfig: %w", err)
		}
		if merged.CurrentContext == "" {
			return nil, fmt.Errorf("no context specified: pass --contexts or --all-contexts, or set a current-context in your kubeconfig")
		}
		if _, ok := sourceFile[merged.CurrentContext]; !ok {
			return nil, fmt.Errorf("current-context %q not found (available: %s)", merged.CurrentContext, strings.Join(order, ", "))
		}
		selected = []string{merged.CurrentContext}
	}

	refs := make([]ClusterRef, 0, len(selected))
	for _, name := range selected {
		if later := shadowed[name]; len(later) > 0 {
			slog.Warn("context defined in more than one kubeconfig file; using the first, as kubectl does",
				"context", name, "using", sourceFile[name], "ignored", strings.Join(later, ", "))
		}
		refs = append(refs, ClusterRef{
			Name:       name,
			Context:    name,
			Kubeconfig: sourceFile[name],
			loadFrom:   rules.Precedence,
		})
	}
	return refs, nil
}

// expandHome replaces a leading "~/" (or a bare "~") with the user's home
// directory, as a shell would; kubeconfig paths in a config file get no shell.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("kubeconfig %s: %w", path, err)
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}
