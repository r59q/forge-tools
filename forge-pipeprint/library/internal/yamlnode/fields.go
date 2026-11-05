package yamlnode

import (
	"slices"

	"go.yaml.in/yaml/v3"
)

// Fields reads a mapping key by key. Keys never taken, or kept after a
// failed read, make up the rest, which callers store as native settings.
type Fields struct {
	pairs []Pair
	used  map[string]bool
}

// NewFields returns Fields over pairs.
func NewFields(pairs []Pair) *Fields {
	return &Fields{pairs: pairs, used: map[string]bool{}}
}

// Take returns the value for key and marks it read, or nil when missing.
func (f *Fields) Take(key string) *yaml.Node {
	value := Get(f.pairs, key)
	if value != nil {
		f.used[key] = true
	}

	return value
}

// Keep returns key to the rest, for values the caller cannot model.
func (f *Fields) Keep(key string) {
	delete(f.used, key)
}

// Rest returns the unread entries as plain values, or nil when none.
func (f *Fields) Rest() map[string]any {
	var rest map[string]any
	for _, pair := range f.pairs {
		if f.used[pair.Key] {
			continue
		}

		if rest == nil {
			rest = map[string]any{}
		}

		rest[pair.Key] = Plain(pair.Value)
	}

	return rest
}

// Value returns v as a node, writing nil as an empty value.
func Value(v any) *yaml.Node {
	if v == nil {
		return Null()
	}

	return Any(v)
}

// Sort orders m's keys by their position in order. The key "*" marks where
// unlisted keys go; they keep their relative order. Without "*" they go
// last.
func (m *Map) Sort(order []string) {
	rank := func(key string) int {
		if i := slices.Index(order, key); i >= 0 {
			return i
		}

		if i := slices.Index(order, "*"); i >= 0 {
			return i
		}

		return len(order)
	}

	type entry struct{ key, value *yaml.Node }
	entries := make([]entry, 0, m.Len())
	for i := 0; i+1 < len(m.Node.Content); i += 2 {
		entries = append(entries, entry{m.Node.Content[i], m.Node.Content[i+1]})
	}

	slices.SortStableFunc(entries, func(a, b entry) int {
		return rank(a.key.Value) - rank(b.key.Value)
	})

	m.Node.Content = m.Node.Content[:0]
	for _, e := range entries {
		m.Node.Content = append(m.Node.Content, e.key, e.value)
	}
}

// AddRest adds native entries in key order and returns the keys it skipped
// because m already has them.
func (m *Map) AddRest(rest map[string]any) []string {
	var skipped []string
	for _, key := range sorted(rest) {
		if m.Has(key) {
			skipped = append(skipped, key)
			continue
		}

		m.Add(key, Value(rest[key]))
	}

	return skipped
}

// TakeString reads a string, or keeps the key when it is not one.
func (f *Fields) TakeString(key string) string {
	value, ok := AsString(f.Take(key))
	f.keepUnless(key, ok)

	return value
}

// TakeStrings reads a string or a list of strings, or keeps the key.
func (f *Fields) TakeStrings(key string) []string {
	value, ok := AsStrings(f.Take(key))
	f.keepUnless(key, ok)

	return value
}

// TakeFlatStrings reads a string or nested lists of strings, or keeps the
// key.
func (f *Fields) TakeFlatStrings(key string) []string {
	value, ok := AsFlatStrings(f.Take(key))
	f.keepUnless(key, ok)

	return value
}

// TakeStringMap reads a mapping of scalars, or keeps the key.
func (f *Fields) TakeStringMap(key string) map[string]string {
	value, ok := AsStringMap(f.Take(key))
	f.keepUnless(key, ok)

	return value
}

// TakeBool reads a boolean, or keeps the key.
func (f *Fields) TakeBool(key string) bool {
	value, ok := AsBool(f.Take(key))
	f.keepUnless(key, ok)

	return value
}

// keepUnless keeps key when a read failed. Keeping a missing key does
// nothing.
func (f *Fields) keepUnless(key string, ok bool) {
	if !ok {
		f.Keep(key)
	}
}
