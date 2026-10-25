package connect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client-side rate limiting defaults, chosen to keep the load on busy API servers low.
const (
	defaultQPS   = 25
	defaultBurst = 50

	// probeConcurrency bounds how many clusters ProbeAll checks at once.
	probeConcurrency = 4
)

// Probe checks whether ref's cluster is reachable within timeout via a
// discovery ServerVersion() call. It NEVER returns an error: unreachable
// clusters are reported through Reachable=false and a non-empty SkipReason,
// so one bad cluster never aborts the run (fail-soft).
func Probe(ctx context.Context, ref ClusterRef, timeout time.Duration) ClusterRef {
	restCfg, err := restConfigFor(ref)
	if err != nil {
		ref.Reachable = false
		ref.SkipReason = err.Error()
		return ref
	}
	restCfg.Timeout = timeout

	disco, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		ref.Reachable = false
		ref.SkipReason = err.Error()
		return ref
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		version string
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		v, err := disco.ServerVersion()
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		resultCh <- result{version: v.GitVersion}
	}()

	start := time.Now()
	defer func() {
		slog.Info("probed cluster", "cluster", ref.Name, "reachable", ref.Reachable, "version", ref.ServerVersion, "took", time.Since(start).Round(time.Millisecond))
	}()
	select {
	case <-probeCtx.Done():
		ref.Reachable = false
		ref.SkipReason = probeCtx.Err().Error()
		if ctx.Err() == nil {
			ref.SkipReason = probeTimeoutReason(timeout)
		}
	case res := <-resultCh:
		if res.err != nil {
			ref.Reachable = false
			ref.SkipReason = res.err.Error()
			if isTimeout(res.err) && ctx.Err() == nil {
				ref.SkipReason = probeTimeoutReason(timeout)
			}
		} else {
			ref.Reachable = true
			ref.ServerVersion = res.version
		}
	}
	return ref
}

// probeTimeoutReason is the SkipReason for a cluster that did not answer in
// time. A credential plugin runs inside the probe, so a login that waits for
// the user (SSO, a browser) or a slow CLI looks exactly like a slow cluster.
func probeTimeoutReason(timeout time.Duration) string {
	return fmt.Sprintf("no answer within --probe-timeout (%s); if this context uses a login plugin, log in first or raise --probe-timeout", timeout)
}

// isTimeout reports whether err is a deadline or network timeout.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// ProbeAll probes every ref concurrently, bounded to probeConcurrency at a
// time, and returns a new slice in the same order as refs.
func ProbeAll(ctx context.Context, refs []ClusterRef, timeout time.Duration) []ClusterRef {
	out := make([]ClusterRef, len(refs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(probeConcurrency)
	for i, ref := range refs {
		g.Go(func() error {
			out[i] = Probe(gctx, ref, timeout)
			return nil
		})
	}
	_ = g.Wait()
	return out
}

// restConfigFor builds a *rest.Config for ref's context, loaded from the
// same kubeconfig merge Resolve used (or ref.Kubeconfig alone), with
// client-side rate-limiting defaults, the read-only transport and the slog
// warning handler applied.
func restConfigFor(ref ClusterRef) (*rest.Config, error) {
	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: ref.Kubeconfig}
	if len(ref.loadFrom) > 0 {
		loadingRules = &clientcmd.ClientConfigLoadingRules{Precedence: ref.loadFrom}
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: ref.Context}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)

	restCfg, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	restCfg.QPS = defaultQPS
	restCfg.Burst = defaultBurst
	restCfg.WarningHandler = slogWarnings{}
	restCfg.Wrap(func(rt http.RoundTripper) http.RoundTripper { return readOnlyTransport{next: rt} })
	return restCfg, nil
}
