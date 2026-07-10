// Package sopsconfig manages the consumer repo's .sops.yaml. The CLI is the sole
// editor of that file: it upserts a per-server creation_rule idempotently while
// preserving the rest of the document (comments, other rules, formatting), so
// operators never hand-edit it and diffs stay minimal on a security-sensitive
// file.
package sopsconfig

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Rule is one creation_rule: a path regex and its comma-separated age recipients.
type Rule struct {
	PathRegex string
	Age       string
}

// UpsertRule adds or updates the creation_rule matching rule.PathRegex in the
// .sops.yaml at path, preserving everything else. A missing file is created.
func UpsertRule(path string, rule Rule) error {
	root, doc, err := load(path)
	if err != nil {
		return err
	}
	rules := findOrCreateSeq(root, "creation_rules")

	for _, m := range rules.Content {
		if m.Kind == yaml.MappingNode && mapGet(m, "path_regex") == rule.PathRegex {
			setScalar(m, "age", rule.Age)
			return write(path, doc)
		}
	}
	rules.Content = append(rules.Content, ruleNode(rule))
	return write(path, doc)
}

// Rules returns the current creation_rules (used for verification/tests).
func Rules(path string) ([]Rule, error) {
	root, _, err := load(path)
	if err != nil {
		return nil, err
	}
	seq := findSeq(root, "creation_rules")
	if seq == nil {
		return nil, nil
	}
	out := make([]Rule, 0, len(seq.Content))
	for _, m := range seq.Content {
		if m.Kind == yaml.MappingNode {
			out = append(out, Rule{PathRegex: mapGet(m, "path_regex"), Age: mapGet(m, "age")})
		}
	}
	return out, nil
}

func load(path string) (root, doc *yaml.Node, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
		return doc.Content[0], doc, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	doc = &yaml.Node{}
	if err := yaml.Unmarshal(data, doc); err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("%s: expected a top-level mapping", path)
	}
	return doc.Content[0], doc, nil
}

func write(path string, doc *yaml.Node) error {
	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("serialising sops config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// findSeq returns the sequence value of key in a mapping node, or nil.
func findSeq(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key && mapping.Content[i+1].Kind == yaml.SequenceNode {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// findOrCreateSeq returns the sequence value of key, creating an empty one if the
// key is absent.
func findOrCreateSeq(mapping *yaml.Node, key string) *yaml.Node {
	if seq := findSeq(mapping, key); seq != nil {
		return seq
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		seq)
	return seq
}

// mapGet reads a scalar value by key from a mapping node.
func mapGet(mapping *yaml.Node, key string) string {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1].Value
		}
	}
	return ""
}

// setScalar sets (or updates) a scalar key on a mapping node.
func setScalar(mapping *yaml.Node, key, value string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1].Value = value
			mapping.Content[i+1].Tag = "!!str"
			mapping.Content[i+1].Style = 0
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: "!!str"})
}

func ruleNode(rule Rule) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	setScalar(m, "path_regex", rule.PathRegex)
	setScalar(m, "age", rule.Age)
	return m
}
