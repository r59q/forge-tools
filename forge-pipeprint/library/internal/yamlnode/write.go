package yamlnode

import (
	"bytes"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// Map builds a mapping node in insertion order.
type Map struct {
	Node *yaml.Node
}

// NewMap returns an empty mapping.
func NewMap() *Map {
	return &Map{Node: &yaml.Node{Kind: yaml.MappingNode}}
}

// Add appends name with value. A nil value is skipped.
func (m *Map) Add(name string, value *yaml.Node) {
	if value == nil {
		return
	}

	m.Node.Content = append(m.Node.Content, key(name), value)
}

// key returns a key node. Its explicit string tag keeps keys such as on
// unquoted, while keys that would read back as another type stay quoted.
func key(name string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
}

// Has reports whether key was added.
func (m *Map) Has(key string) bool {
	for i := 0; i < len(m.Node.Content); i += 2 {
		if m.Node.Content[i].Value == key {
			return true
		}
	}

	return false
}

// Len returns the number of entries.
func (m *Map) Len() int {
	return len(m.Node.Content) / 2
}

// Str returns a string node, quoted when needed.
func Str(s string) *yaml.Node {
	return Any(s)
}

// Scalar returns a string node written plain when it reads back as the
// same text, such as 0 or true. Use it for values forges read as strings.
func Scalar(s string) *yaml.Node {
	if s == "true" || s == "false" {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: s}
	}

	if i, err := strconv.Atoi(s); err == nil && strconv.Itoa(i) == s {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: s}
	}

	return Str(s)
}

// Int returns an integer node.
func Int(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(i)}
}

// Bool returns a boolean node.
func Bool(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(b)}
}

// Null returns an empty node, written as nothing after the key.
func Null() *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
}

// Seq returns a sequence of strings.
func Seq(values []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode}
	for _, v := range values {
		n.Content = append(n.Content, Str(v))
	}

	return n
}

// Strings returns one string as a scalar and several as a sequence.
func Strings(values []string) *yaml.Node {
	if len(values) == 1 {
		return Str(values[0])
	}

	return Seq(values)
}

// StringMap returns a mapping with sorted keys and Scalar values, or nil
// when m is empty.
func StringMap(m map[string]string) *yaml.Node {
	if len(m) == 0 {
		return nil
	}

	out := NewMap()
	for _, name := range sorted(m) {
		out.Add(name, Scalar(m[name]))
	}

	return out.Node
}

// Any encodes a plain Go value.
func Any(v any) *yaml.Node {
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		return Null()
	}

	return n
}

// Encode writes n as a YAML document with two-space indents.
func Encode(n *yaml.Node) ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)

	if err := encoder.Encode(n); err != nil {
		return nil, err
	}

	if err := encoder.Close(); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func sorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}
