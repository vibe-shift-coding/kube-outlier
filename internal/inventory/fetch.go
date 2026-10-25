package inventory

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/vibe-shift-coding/kube-outlier/internal/identity"
)

// FetchBody is Pass B: ONE targeted full-object Get, never a list —
// there is deliberately no list-based variant, so the metadata-only
// performance model of Pass A cannot erode. Used only by --compare-content
// for the content.kinds resources it compares. 404s pass through
// unwrapped-compatible (errors.Is/apierrors.IsNotFound work), so callers can
// report "vanished since Pass A" and continue.
func FetchBody(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, key identity.ResourceKey) (map[string]interface{}, error) {
	ri := dyn.Resource(gvr)
	get := ri.Get
	if key.Namespace != "" {
		get = ri.Namespace(key.Namespace).Get
	}
	obj, err := get(ctx, key.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", key.String(), err)
	}
	return obj.UnstructuredContent(), nil
}
