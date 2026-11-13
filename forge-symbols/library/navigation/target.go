package navigation

import (
	"fmt"

	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// Target identifies what to navigate from: a name, a path with a position,
// or both, in which case the name narrows what the position selects. A
// name with a path but no position ranks results in that file first.
type Target struct {
	Name string `json:"name,omitempty"`

	// Kind, when set, keeps only definitions of this kind, both when
	// resolving the target and in the results of Outgoing and Definitions.
	Kind string `json:"kind,omitempty"`

	Path string `json:"path,omitempty"`

	// Line and Column locate a position in Path, as in symbols.Range.
	// Set both or neither.
	Line   int `json:"line,omitempty"`
	Column int `json:"column,omitempty"`
}

// anchor is a resolved target: the name to look for, and where it was
// asked from, for ranking.
type anchor struct {
	name      string
	kind      string
	path      string
	container string

	// definition is the definition at the position, if any.
	definition *location
}

// location is a definition or reference in one of the request's files.
type location struct {
	file  int
	index int
}

// position is a one-based line and UTF-16 column.
type position struct {
	line   int
	column int
}

// resolve turns a target into an anchor. A position selects, in order: the
// innermost definition whose name it is on, the reference it is on, or the
// innermost definition around it.
func resolve(files []symbols.Analysis, target Target) (anchor, error) {
	if err := target.check(); err != nil {
		return anchor{}, err
	}

	resolved := anchor{name: target.Name, kind: target.Kind, path: target.Path}
	if target.Line == 0 {
		return resolved, nil
	}

	for file, analysis := range files {
		if analysis.Path != target.Path {
			continue
		}
		if resolved, ok := target.at(analysis, file); ok {
			return resolved, nil
		}
	}
	return anchor{}, ErrNotFound
}

func (t Target) check() error {
	hasPosition := t.Line > 0 && t.Column > 0
	partialPosition := !hasPosition && (t.Line != 0 || t.Column != 0)

	switch {
	case partialPosition:
		return fmt.Errorf("%w: set both line and column", ErrInvalidTarget)
	case hasPosition && t.Path == "":
		return fmt.Errorf("%w: a position needs a path", ErrInvalidTarget)
	case !hasPosition && t.Name == "":
		return fmt.Errorf("%w: set a name, or a path and position", ErrInvalidTarget)
	}
	return nil
}

func (t Target) at(analysis symbols.Analysis, file int) (anchor, bool) {
	at := position{line: t.Line, column: t.Column}

	onName := func(d symbols.Definition) bool { return at.in(d.NameRange) }
	if index, ok := t.innermost(analysis, onName); ok {
		return t.fromDefinition(analysis, file, index), true
	}

	if index, ok := t.reference(analysis, at); ok {
		return t.fromReference(analysis, index), true
	}

	around := func(d symbols.Definition) bool { return at.in(d.Range) }
	if index, ok := t.innermost(analysis, around); ok {
		return t.fromDefinition(analysis, file, index), true
	}
	return anchor{}, false
}

// innermost returns the last matching definition. Definitions are in
// source order, outer before inner, so that is the innermost one.
func (t Target) innermost(analysis symbols.Analysis, keep func(symbols.Definition) bool) (int, bool) {
	found := symbols.None

	for index, definition := range analysis.Definitions {
		if t.matches(definition) && keep(definition) {
			found = index
		}
	}
	return found, found != symbols.None
}

func (t Target) reference(analysis symbols.Analysis, at position) (int, bool) {
	for index, reference := range analysis.References {
		named := t.Name == "" || reference.Name == t.Name
		if named && at.in(reference.Range) {
			return index, true
		}
	}
	return symbols.None, false
}

func (t Target) matches(definition symbols.Definition) bool {
	named := t.Name == "" || definition.Name == t.Name
	kinded := t.Kind == "" || definition.Kind == t.Kind
	return named && kinded
}

func (t Target) fromDefinition(analysis symbols.Analysis, file, index int) anchor {
	definition := analysis.Definitions[index]

	return anchor{
		name:       definition.Name,
		kind:       t.Kind,
		path:       analysis.Path,
		container:  definition.Container,
		definition: &location{file: file, index: index},
	}
}

// fromReference anchors at the container of the definition around the
// reference.
func (t Target) fromReference(analysis symbols.Analysis, index int) anchor {
	reference := analysis.References[index]
	enclosing, _ := analysis.Enclosing(reference)

	return anchor{
		name:      reference.Name,
		kind:      t.Kind,
		path:      analysis.Path,
		container: enclosing.Container,
	}
}

// in reports whether the position is in the range, counting its end, so a
// cursor just after a name selects it.
func (p position) in(r symbols.Range) bool {
	start := position{line: r.StartLine, column: r.StartColumn}
	end := position{line: r.EndLine, column: r.EndColumn}
	return !p.before(start) && !end.before(p)
}

func (p position) before(other position) bool {
	if p.line != other.line {
		return p.line < other.line
	}
	return p.column < other.column
}
