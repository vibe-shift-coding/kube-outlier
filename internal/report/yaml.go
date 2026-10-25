package report

import (
	"fmt"
	"io"

	"github.com/vibe-shift-coding/kube-outlier/internal/orderedyaml"
)

// RenderYAML emits the §3 report envelope as YAML: the identical structure,
// field names and field order as RenderJSON (schemaVersion first).
func RenderYAML(w io.Writer, rep *Report) error {
	raw, err := orderedyaml.Marshal(rep)
	if err != nil {
		return fmt.Errorf("marshalling report: %w", err)
	}
	_, err = w.Write(raw)
	return err
}
