// Package yamlnode reads and writes YAML node trees in file order, with
// aliases and merge keys resolved.
package yamlnode

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ErrSyntax wraps YAML syntax errors.
var ErrSyntax = errors.New("invalid YAML")

// Pair is one mapping entry.
type Pair struct {
	Key   string
	Value *yaml.Node
}

// Load parses data and returns its top-level mapping.
func Load(data []byte) ([]Pair, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSyntax, err)
	}

	pairs, ok := Pairs(&doc)
	if !ok {
		return nil, fmt.Errorf("%w: the file must be a mapping", ErrSyntax)
	}

	return pairs, nil
}

// Resolve follows aliases and unwraps documents.
func Resolve(n *yaml.Node) *yaml.Node {
	for n != nil {
		switch n.Kind {
		case yaml.AliasNode:
			n = n.Alias
		case yaml.DocumentNode:
			if len(n.Content) == 0 {
				return nil
			}

			n = n.Content[0]
		default:
			return n
		}
	}

	return nil
}

// IsNull reports whether n is missing or null.
func IsNull(n *yaml.Node) bool {
	n = Resolve(n)
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

// Pairs returns a mapping's entries in order. Merge keys (<<) are expanded;
// explicit keys override merged ones in place.
func Pairs(n *yaml.Node) ([]Pair, bool) {
	n = Resolve(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, false
	}

	var pairs []Pair
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := Resolve(n.Content[i]), n.Content[i+1]
		if key == nil || key.Kind != yaml.ScalarNode {
			return nil, false
		}

		if key.Tag == "!!merge" || key.Value == "<<" {
			merged, ok := mergeSources(value)
			if !ok {
				return nil, false
			}

			pairs = put(pairs, merged...)

			continue
		}

		pairs = put(pairs, Pair{Key: key.Value, Value: value})
	}

	return pairs, true
}

// mergeSources returns the entries a merge key brings in. In a list of
// mappings, earlier mappings win.
func mergeSources(n *yaml.Node) ([]Pair, bool) {
	n = Resolve(n)
	if n != nil && n.Kind == yaml.SequenceNode {
		var all []Pair
		for _, item := range n.Content {
			pairs, ok := Pairs(item)
			if !ok {
				return nil, false
			}

			for _, pair := range pairs {
				if index(all, pair.Key) < 0 {
					all = append(all, pair)
				}
			}
		}

		return all, true
	}

	return Pairs(n)
}

// put adds entries, replacing the value of an existing key in place.
func put(pairs []Pair, entries ...Pair) []Pair {
	for _, entry := range entries {
		if i := index(pairs, entry.Key); i >= 0 {
			pairs[i].Value = entry.Value
			continue
		}

		pairs = append(pairs, entry)
	}

	return pairs
}

func index(pairs []Pair, key string) int {
	for i, pair := range pairs {
		if pair.Key == key {
			return i
		}
	}

	return -1
}

// Get returns the value for key, or nil.
func Get(pairs []Pair, key string) *yaml.Node {
	if i := index(pairs, key); i >= 0 {
		return pairs[i].Value
	}

	return nil
}

// AsString returns a non-null scalar's text.
func AsString(n *yaml.Node) (string, bool) {
	n = Resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode || IsNull(n) || custom(n) {
		return "", false
	}

	return n.Value, true
}

// AsStrings returns a scalar as one string, or a sequence of scalars.
func AsStrings(n *yaml.Node) ([]string, bool) {
	if s, ok := AsString(n); ok {
		return []string{s}, true
	}

	n = Resolve(n)
	if n == nil || n.Kind != yaml.SequenceNode || custom(n) {
		return nil, false
	}

	values := make([]string, 0, len(n.Content))
	for _, item := range n.Content {
		s, ok := AsString(item)
		if !ok {
			return nil, false
		}

		values = append(values, s)
	}

	return values, true
}

// AsStringMap returns a mapping of scalars, keeping each scalar's text.
func AsStringMap(n *yaml.Node) (map[string]string, bool) {
	pairs, ok := Pairs(n)
	if !ok {
		return nil, false
	}

	values := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		s, ok := AsString(pair.Value)
		if !ok {
			return nil, false
		}

		values[pair.Key] = s
	}

	return values, true
}

// AsBool returns a boolean scalar.
func AsBool(n *yaml.Node) (bool, bool) {
	var b bool
	n = Resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode || n.Decode(&b) != nil {
		return false, false
	}

	return b, true
}

// AsInt returns an integer scalar.
func AsInt(n *yaml.Node) (int, bool) {
	var i int
	n = Resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode || n.Decode(&i) != nil {
		return 0, false
	}

	return i, true
}

// Plain returns n as plain Go values: maps, slices and scalars.
func Plain(n *yaml.Node) any {
	var v any
	if n == nil || n.Decode(&v) != nil {
		return nil
	}

	return v
}

// AsFlatStrings returns a scalar or a sequence of scalars, flattening nested
// sequences such as those that aliases bring in.
func AsFlatStrings(n *yaml.Node) ([]string, bool) {
	if s, ok := AsString(n); ok {
		return []string{s}, true
	}

	n = Resolve(n)
	if n == nil || n.Kind != yaml.SequenceNode || custom(n) {
		return nil, false
	}

	var values []string
	for _, item := range n.Content {
		nested, ok := AsFlatStrings(item)
		if !ok {
			return nil, false
		}

		values = append(values, nested...)
	}

	return values, true
}

// custom reports whether n has an application tag, such as GitLab's
// !reference, which plain values cannot hold.
func custom(n *yaml.Node) bool {
	return strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!")
}

// CustomTags describes every application tag under pairs, such as
// "!reference on line 4", in file order.
func CustomTags(pairs []Pair) []string {
	var found []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}

		if custom(n) {
			found = append(found, fmt.Sprintf("%s on line %d", n.Tag, n.Line))
		}

		for _, child := range n.Content {
			walk(child)
		}
	}

	for _, pair := range pairs {
		walk(pair.Value)
	}

	return found
}
