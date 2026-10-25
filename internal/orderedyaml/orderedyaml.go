// Package orderedyaml marshals a value to YAML with the same structure and
// field names as its JSON encoding, keeping struct fields in declaration order
// (sigs.k8s.io/yaml sorts every key). Reports read top-down — schemaVersion
// first — and a baseline keeps its hand-edited exceptions above the generated
// entries. Map keys stay sorted, as encoding/json sorts them.
package orderedyaml

import (
	"bytes"
	"encoding/json"
	"fmt"

	yaml "sigs.k8s.io/yaml/goyaml.v3"
)

// Marshal encodes v as JSON, then re-renders that document as block-style YAML
// with 2-space indentation. Strings that would read as another type ("true",
// "1", timestamps) stay quoted.
func Marshal(v interface{}) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("re-reading JSON as YAML: %w", err)
	}
	plainStyle(&doc)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// plainStyle drops the flow and quoting styles the JSON input implies, so the
// encoder chooses block style and quotes only where a plain scalar would
// change type.
func plainStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		plainStyle(c)
	}
}
