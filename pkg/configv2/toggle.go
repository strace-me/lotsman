package configv2

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// SetRuleEnabled switches one rule on or off by editing its YAML file in place, and
// reports whether a rule of that id was found.
//
// It edits the YAML NODE rather than round-tripping through Rule on purpose. A rule's
// structured re-save legitimately rewrites the file — the operator asked for that —
// but a tile toggle is a one-bit change, and rewriting the whole file for it would
// expand every zero value the operator never wrote and drop every comment they did. A
// switch on the dashboard must not reformat the file behind it.
//
// Switching ON removes the key instead of writing `enabled: true`, so a file that was
// never touched reads as if it never had been.
func SetRuleEnabled(data []byte, id string, enabled bool) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("config: yaml: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, false, fmt.Errorf("config: empty rule file")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("config: rule file top level is not a mapping")
	}
	got := mapValue(root, "id")
	if got == nil || got.Value != id {
		return nil, false, nil
	}
	setEnabled(root, enabled)

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

// setEnabled writes, updates or removes the `enabled` key on a rule mapping.
func setEnabled(rule *yaml.Node, enabled bool) {
	for i := 0; i+1 < len(rule.Content); i += 2 {
		if rule.Content[i].Value != "enabled" {
			continue
		}
		if !enabled {
			rule.Content = append(rule.Content[:i], rule.Content[i+2:]...)
			return
		}
		rule.Content[i+1].Tag = "!!bool"
		rule.Content[i+1].Value = "true"
		return
	}
	if !enabled {
		return // already absent, which is what "on" means
	}
	pair := []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "enabled"},
		{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
	}
	// Straight after `id`: a flag that decides whether the rest of the block means
	// anything belongs where it will be read, not buried under thirty lines of it.
	at := 0
	for i := 0; i+1 < len(rule.Content); i += 2 {
		if rule.Content[i].Value == "id" {
			at = i + 2
			break
		}
	}
	rule.Content = append(rule.Content[:at], append(pair, rule.Content[at:]...)...)
}
