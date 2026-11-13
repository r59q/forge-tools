package symbols

import (
	"cmp"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-symbols/library/internal/treesitter"
)

// Capture names of a tag query.
const (
	captureName      = "name"
	captureContainer = "container"
	prefixDefinition = "definition."
	prefixReference  = "reference."
)

// span is a byte range of the source.
type span struct {
	start int
	end   int
}

// tag is one usable match of a tag query: a named definition or reference.
// Pattern is the index of the query pattern that matched; when several
// patterns tag the same name, the first pattern of the query wins.
type tag struct {
	kind      string
	name      span
	node      span
	container *span
	pattern   int
}

// newAnalysis turns tag query matches into definitions and references.
func newAnalysis(matches []treesitter.Match, source []byte) Analysis {
	definitions, references := readTags(matches)
	definitions = mergeDefinitions(definitions)
	references = dropNames(mergeReferences(references), definitions)

	located := newPositions(source, offsets(definitions, references))
	text := func(s span) string { return string(source[s.start:s.end]) }

	analysis := Analysis{
		Definitions: make([]Definition, 0, len(definitions)),
		References:  make([]Reference, 0, len(references)),
	}

	parents := parents(definitions)
	for index, definition := range definitions {
		analysis.Definitions = append(analysis.Definitions, Definition{
			Name:      text(definition.name),
			Kind:      definition.kind,
			NameRange: located.span(definition.name.start, definition.name.end),
			Range:     located.span(definition.node.start, definition.node.end),
			Container: container(definition, parents[index], definitions, text),
			Parent:    parents[index],
		})
	}

	enclosing := enclosing(references, definitions)
	for index, reference := range references {
		analysis.References = append(analysis.References, Reference{
			Name:      text(reference.name),
			Kind:      reference.kind,
			Range:     located.span(reference.name.start, reference.name.end),
			Enclosing: enclosing[index],
		})
	}
	return analysis
}

// readTags splits the matches into definitions and references. Matches
// without a name or a role are not tags.
func readTags(matches []treesitter.Match) (definitions, references []tag) {
	for _, match := range matches {
		found, definition, ok := readTag(match)
		switch {
		case !ok:
		case definition:
			definitions = append(definitions, found)
		default:
			references = append(references, found)
		}
	}
	return definitions, references
}

// readTag reports whether the match is a tag, and whether it is a definition.
func readTag(match treesitter.Match) (found tag, definition, ok bool) {
	named, kinded := false, false
	found.pattern = match.Pattern

	for _, capture := range match.Captures {
		captured := span{start: capture.Start, end: capture.End}

		switch {
		case capture.Name == captureName:
			found.name, named = captured, true
		case capture.Name == captureContainer:
			found.container = &captured
		case strings.HasPrefix(capture.Name, prefixDefinition):
			found.kind, found.node = strings.TrimPrefix(capture.Name, prefixDefinition), captured
			definition, kinded = true, true
		case strings.HasPrefix(capture.Name, prefixReference):
			found.kind, found.node = strings.TrimPrefix(capture.Name, prefixReference), captured
			definition, kinded = false, true
		}
	}
	return found, definition, named && kinded
}

// mergeDefinitions merges matches of the same name, which name one
// definition, and keeps the match of the first pattern, so a pattern can
// widen the node of a more general one, as with Python decorators. Then
// it sorts definitions in source order, outer before inner.
func mergeDefinitions(definitions []tag) []tag {
	slices.SortStableFunc(definitions, func(a, b tag) int {
		return cmp.Or(
			cmp.Compare(a.name.start, b.name.start),
			cmp.Compare(a.name.end, b.name.end),
			cmp.Compare(a.pattern, b.pattern),
		)
	})

	merged := definitions[:0]
	for _, definition := range definitions {
		last := len(merged) - 1
		if last < 0 || merged[last].name != definition.name {
			merged = append(merged, definition)
			continue
		}
		if merged[last].container == nil {
			merged[last].container = definition.container
		}
	}

	slices.SortStableFunc(merged, func(a, b tag) int {
		return cmp.Or(
			cmp.Compare(a.node.start, b.node.start),
			cmp.Compare(b.node.end, a.node.end),
			cmp.Compare(a.name.start, b.name.start),
		)
	})
	return merged
}

// mergeReferences sorts references in source order and keeps the match of
// each name from the first pattern.
func mergeReferences(references []tag) []tag {
	slices.SortStableFunc(references, func(a, b tag) int {
		return cmp.Or(
			cmp.Compare(a.name.start, b.name.start),
			cmp.Compare(a.name.end, b.name.end),
			cmp.Compare(a.pattern, b.pattern),
		)
	})

	return slices.CompactFunc(references, func(a, b tag) bool {
		return a.name == b.name
	})
}

// dropNames removes references that are the name of a definition.
func dropNames(references, definitions []tag) []tag {
	names := make(map[span]bool, len(definitions))
	for _, definition := range definitions {
		names[definition.name] = true
	}

	return slices.DeleteFunc(references, func(reference tag) bool {
		return names[reference.name]
	})
}

func offsets(definitions, references []tag) []int {
	offsets := make([]int, 0, 4*len(definitions)+2*len(references))

	for _, definition := range definitions {
		offsets = append(offsets, definition.name.start, definition.name.end, definition.node.start, definition.node.end)
	}
	for _, reference := range references {
		offsets = append(offsets, reference.name.start, reference.name.end)
	}
	return offsets
}

// container names the type a definition belongs to: the captured
// container, or else the parent definition.
func container(definition tag, parent int, definitions []tag, text func(span) string) string {
	if definition.container != nil {
		return text(*definition.container)
	}
	if parent != None {
		return text(definitions[parent].name)
	}
	return ""
}

// parents returns, for each sorted definition, the index of the innermost
// definition that strictly encloses it, or None.
func parents(definitions []tag) []int {
	parents := make([]int, len(definitions))
	open := newNesting(definitions)

	for index, definition := range definitions {
		parents[index] = open.enter(index, definition.node)
	}
	return parents
}

// enclosing returns, for each sorted reference, the index of the innermost
// definition that contains its name, or None.
func enclosing(references, definitions []tag) []int {
	enclosing := make([]int, len(references))
	open := newNesting(definitions)
	next := 0

	for index, reference := range references {
		for ; next < len(definitions) && definitions[next].node.start <= reference.name.start; next++ {
			open.enter(next, definitions[next].node)
		}
		enclosing[index] = open.around(reference.name)
	}
	return enclosing
}

// nesting tracks the definitions that are open at a point of a sweep
// through the source. Nodes of one syntax tree nest or are disjoint.
type nesting struct {
	definitions []tag
	stack       []int
}

func newNesting(definitions []tag) *nesting {
	return &nesting{definitions: definitions}
}

// enter opens a definition and returns the one that strictly encloses it.
func (n *nesting) enter(index int, node span) int {
	parent := n.close(func(open span) bool {
		return open != node && open.contains(node)
	})

	n.stack = append(n.stack, index)
	return parent
}

// around returns the innermost open definition that contains the span.
func (n *nesting) around(inner span) int {
	return n.close(func(open span) bool {
		return open.contains(inner)
	})
}

// close pops open definitions until one is kept, and returns it.
func (n *nesting) close(keep func(open span) bool) int {
	for len(n.stack) > 0 {
		top := n.stack[len(n.stack)-1]
		if keep(n.definitions[top].node) {
			return top
		}
		n.stack = n.stack[:len(n.stack)-1]
	}
	return None
}

func (s span) contains(inner span) bool {
	return s.start <= inner.start && inner.end <= s.end
}
