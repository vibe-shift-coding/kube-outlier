// Package connect resolves the requested kubeconfig context(s) into cluster
// references, probes them fail-soft for reachability, and builds read-only
// client bundles for reachable clusters.
package connect

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	k8sdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
)

// ClusterRef is one member of the requested fleet, after resolution and probing.
type ClusterRef struct {
	Name          string // stable display name; unique within a run (context name by default)
	Context       string // kubeconfig context name
	Kubeconfig    string // path of the kubeconfig file it came from
	Reachable     bool
	ServerVersion string // e.g. "v1.30.2"; "" when unreachable
	SkipReason    string // non-empty iff !Reachable; human-readable cause

	// loadFrom is every kubeconfig file of the merge Resolve performed, in
	// precedence order. Clients load the context from the same merge, so a
	// context may use a user or cluster defined in another file, as with
	// KUBECONFIG=a:b. Empty: load Kubeconfig alone.
	loadFrom []string
}

// Clients is the read-only client bundle for one reachable cluster.
type Clients struct {
	Dynamic   dynamic.Interface
	Metadata  metadata.Interface
	Discovery k8sdiscovery.DiscoveryInterface
}

// New builds a read-only client bundle for ref. Only called for Reachable
// refs. Every request is bounded by requestTimeout, and the transport refuses
// any HTTP method other than GET and HEAD (see readOnlyTransport).
func New(ref ClusterRef, requestTimeout time.Duration) (*Clients, error) {
	restCfg, err := restConfigFor(ref)
	if err != nil {
		return nil, fmt.Errorf("building client config for %s: %w", ref.Name, err)
	}
	restCfg.Timeout = requestTimeout

	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client for %s: %w", ref.Name, err)
	}

	metaClient, err := metadata.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building metadata client for %s: %w", ref.Name, err)
	}

	disco, err := k8sdiscovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building discovery client for %s: %w", ref.Name, err)
	}

	return &Clients{
		Dynamic:   dyn,
		Metadata:  metaClient,
		Discovery: disco,
	}, nil
}

// readOnlyTransport rejects every request whose method could change cluster
// state. It is the runtime half of the read-only rule; the static half is
// scripts/check-readonly.sh.
type readOnlyTransport struct {
	next http.RoundTripper
}

// RoundTrip forwards GET and HEAD requests and refuses everything else.
func (t readOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.Method {
	case http.MethodGet, http.MethodHead:
		return t.next.RoundTrip(req)
	default:
		return nil, fmt.Errorf("kube-outlier is read-only: refused %s %s", req.Method, req.URL.Path)
	}
}

// WrappedRoundTripper exposes the wrapped transport
// (k8s.io/apimachinery/pkg/util/net.RoundTripperWrapper), so client-go can
// cancel a timed-out request on it instead of logging that it cannot.
// Requests are still only ever sent through RoundTrip.
func (t readOnlyTransport) WrappedRoundTripper() http.RoundTripper {
	return t.next
}

// slogWarnings passes the API server's warning headers (a deprecated API
// version, for example) to slog, so they appear like the tool's own warnings.
type slogWarnings struct{}

// HandleWarningHeader implements rest.WarningHandler.
func (slogWarnings) HandleWarningHeader(code int, agent string, text string) {
	if code != 299 || text == "" {
		return
	}
	slog.Warn("API server warning", "warning", text)
}
