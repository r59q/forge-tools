// Package portable reads the shapes that several dialects share, such as
// environments and matrix axes, into Pipeprint values.
package portable

import (
	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	"go.yaml.in/yaml/v3"
)

// Environment reads key when it is a name or a {name, url} map, or keeps
// it.
func Environment(f *yamlnode.Fields, key string) *pipeprint.Environment {
	v := f.Take(key)
	if v == nil {
		return nil
	}

	if name, ok := yamlnode.AsString(v); ok {
		return &pipeprint.Environment{Name: name}
	}

	pairs, _ := yamlnode.Pairs(v)
	ef := yamlnode.NewFields(pairs)
	env := &pipeprint.Environment{Name: ef.TakeString("name"), URL: ef.TakeString("url")}
	if env.Name == "" || ef.Rest() != nil {
		f.Keep(key)
		return nil
	}

	return env
}

// Image reads key when it is an image name or a map holding only nameKey,
// or keeps it.
func Image(f *yamlnode.Fields, key, nameKey string) string {
	v := f.Take(key)
	if v == nil {
		return ""
	}

	if image, ok := yamlnode.AsString(v); ok {
		return image
	}

	pairs, _ := yamlnode.Pairs(v)
	if image, ok := yamlnode.AsString(yamlnode.Get(pairs, nameKey)); ok && len(pairs) == 1 {
		return image
	}

	f.Keep(key)

	return ""
}

// MatrixAxes reads a mapping of scalar lists. Keys in reserved fail.
func MatrixAxes(n *yaml.Node, reserved ...string) (map[string][]string, bool) {
	pairs, ok := yamlnode.Pairs(n)
	if !ok || len(pairs) == 0 {
		return nil, false
	}

	axes := map[string][]string{}
	for _, pair := range pairs {
		values, ok := yamlnode.AsStrings(pair.Value)
		if !ok || contains(reserved, pair.Key) {
			return nil, false
		}

		axes[pair.Key] = values
	}

	return axes, true
}

// SetNative stores rest in n's block for dialect.
func SetNative(n *pipeprint.Native, dialect pipeprint.Dialect, rest map[string]any) {
	for key, value := range rest {
		n.Set(dialect, key, value)
	}
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}

	return false
}

// EnvironmentNode writes env as a name, or as {name, url} when it has a URL.
func EnvironmentNode(env *pipeprint.Environment) *yaml.Node {
	if env == nil {
		return nil
	}

	if env.URL == "" {
		return yamlnode.Str(env.Name)
	}

	m := yamlnode.NewMap()
	m.Add("name", yamlnode.Str(env.Name))
	m.Add("url", yamlnode.Str(env.URL))

	return m.Node
}

// AxesNode writes matrix axes in key order, or nil when there are none.
func AxesNode(axes map[string][]string) *yaml.Node {
	if len(axes) == 0 {
		return nil
	}

	m := yamlnode.NewMap()
	for _, axis := range pipeprint.SortedKeys(axes) {
		values := &yaml.Node{Kind: yaml.SequenceNode}
		for _, v := range axes[axis] {
			values.Content = append(values.Content, yamlnode.Scalar(v))
		}

		m.Add(axis, values)
	}

	return m.Node
}
