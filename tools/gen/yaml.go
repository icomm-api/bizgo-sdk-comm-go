package main

import (
	"fmt"
	"os"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// node helpers over yaml.Node, keeping the key order of the spec.

func loadSpec(path string) (*yaml.Node, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: path of the vendored spec, given by the developer
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, fmt.Errorf("%s: not a YAML document", path)
	}
	return doc.Content[0], nil
}

func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// get returns the value of key in a mapping node, or nil.
func get(n *yaml.Node, key string) *yaml.Node {
	n = resolve(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return resolve(n.Content[i+1])
		}
	}
	return nil
}

// path follows get for each key.
func path(n *yaml.Node, keys ...string) *yaml.Node {
	for _, k := range keys {
		n = get(n, k)
	}
	return n
}

// keys returns the keys of a mapping node in document order.
func keys(n *yaml.Node) []string {
	n = resolve(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]string, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, n.Content[i].Value)
	}
	return out
}

func items(n *yaml.Node) []*yaml.Node {
	n = resolve(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]*yaml.Node, len(n.Content))
	for i, c := range n.Content {
		out[i] = resolve(c)
	}
	return out
}

func str(n *yaml.Node) string {
	n = resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

func strs(n *yaml.Node) []string {
	var out []string
	for _, c := range items(n) {
		out = append(out, str(c))
	}
	return out
}

func intp(n *yaml.Node) (*int64, error) {
	if n == nil {
		return nil, nil
	}
	v, err := strconv.ParseInt(str(n), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("line %d: not an integer: %q", n.Line, str(n))
	}
	return &v, nil
}

// toAny converts a node into plain Go values for JSON output.
func toAny(n *yaml.Node) (any, error) {
	var v any
	if err := resolve(n).Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
