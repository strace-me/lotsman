package config

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// SetServiceEnabledYAML switches one declared service on or off by editing the
// raw config text, and reports whether a service of that name was found.
//
// It edits the YAML NODE rather than round-tripping through Document on purpose.
// The structured editor's save legitimately rewrites the file — the operator
// asked for that — but a tile toggle is a one-bit change, and rewriting the whole
// document for it would expand every zero value the operator never wrote and drop
// every comment they did. A switch on the dashboard must not reformat the file
// behind it.
//
// Switching ON removes the key instead of writing `disabled: false`, so a config
// that was never touched reads as if it never had been.
func SetServiceEnabledYAML(data []byte, name string, enabled bool) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("config: yaml: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, false, fmt.Errorf("config: empty document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("config: top level is not a mapping")
	}
	services := mapValue(root, "services")
	if services == nil || services.Kind != yaml.SequenceNode {
		return nil, false, fmt.Errorf("config: no services section")
	}
	svc := findService(services, name)
	if svc == nil {
		return nil, false, nil
	}
	setDisabled(svc, !enabled)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, false, fmt.Errorf("config: re-encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, false, fmt.Errorf("config: re-encode: %w", err)
	}
	return buf.Bytes(), true, nil
}

// mapValue returns the value node for key in a mapping, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// findService returns the mapping node of the service declaration named name.
func findService(seq *yaml.Node, name string) *yaml.Node {
	for _, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		if n := mapValue(item, "name"); n != nil && n.Value == name {
			return item
		}
	}
	return nil
}

// setDisabled writes, updates or removes the `disabled` key on a service mapping.
func setDisabled(svc *yaml.Node, disabled bool) {
	for i := 0; i+1 < len(svc.Content); i += 2 {
		if svc.Content[i].Value != "disabled" {
			continue
		}
		if !disabled {
			svc.Content = append(svc.Content[:i], svc.Content[i+2:]...)
			return
		}
		svc.Content[i+1].Tag = "!!bool"
		svc.Content[i+1].Value = "true"
		return
	}
	if !disabled {
		return // already absent, which is what "on" means
	}
	pair := []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "disabled"},
		{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
	}
	// Straight after `name`: a flag that decides whether the rest of the block means
	// anything belongs where it will be read, not buried under thirty lines of it.
	at := 0
	for i := 0; i+1 < len(svc.Content); i += 2 {
		if svc.Content[i].Value == "name" {
			at = i + 2
			break
		}
	}
	svc.Content = append(svc.Content[:at], append(pair, svc.Content[at:]...)...)
}
