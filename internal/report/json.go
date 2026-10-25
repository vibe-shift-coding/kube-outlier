package report

import (
	"encoding/json"
	"io"
)

// RenderJSON writes rep as 2-space-indented JSON with a trailing newline
// (json.Encoder.Encode always appends one). HTML escaping is disabled so
// values containing <, > or & (e.g. an arbitrary cluster skipReason) appear
// verbatim rather than as <-style escapes — the JSON is an integration
// surface, not embedded in HTML.
func RenderJSON(w io.Writer, rep *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(rep)
}
