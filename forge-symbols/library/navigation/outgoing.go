package navigation

import (
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// OutgoingResult lists, per target definition, the references in it.
type OutgoingResult struct {
	// Name is the name the target resolved to.
	Name   string          `json:"name"`
	Groups []OutgoingGroup `json:"groups"`

	// Truncated reports that Limits.Results cut the groups or references.
	Truncated bool `json:"truncated"`
}

// OutgoingGroup is one target definition and the references in it, in
// source order.
type OutgoingGroup struct {
	Definition DefinitionResult  `json:"definition"`
	References []ReferenceResult `json:"references"`
}

// Outgoing finds the references inside a target definition, such as the
// calls in a method body. References inside definitions nested in the
// target, such as local functions, are included and marked Nested.
//
// A position on or in a definition targets that definition alone. A name,
// or a position on a reference, targets every definition of that name,
// ranked as by Definitions, with one group each.
//
// It returns the errors of Definitions.
func Outgoing(files []symbols.Analysis, target Target, limits Limits) (OutgoingResult, error) {
	resolved, limits, err := prepare(files, target, limits)
	if err != nil {
		return OutgoingResult{}, err
	}

	result := OutgoingResult{Name: resolved.name}
	remaining := budget{left: limits.Results}

	for _, definition := range resolved.targets(files) {
		references := outgoing(files[definition.file], definition.index)

		kept, ok := remaining.group(len(references))
		if !ok {
			break
		}
		result.Groups = append(result.Groups, OutgoingGroup{
			Definition: definition.result,
			References: references[:kept],
		})
	}

	result.Truncated = remaining.truncated
	return result, nil
}

// targets returns the definition at the position, or else all definitions
// named like the anchor.
func (a anchor) targets(files []symbols.Analysis) []candidate {
	if a.definition == nil {
		return a.definitions(files)
	}

	at := *a.definition
	return []candidate{a.candidate(files[at.file], at.file, at.index)}
}

// outgoing returns the references in the definition at index of the file.
func outgoing(analysis symbols.Analysis, index int) []ReferenceResult {
	var found []ReferenceResult

	for at, reference := range analysis.References {
		if !within(analysis, reference.Enclosing, index) {
			continue
		}
		found = append(found, ReferenceResult{
			Path:      analysis.Path,
			Index:     at,
			Reference: reference,
			Nested:    reference.Enclosing != index,
			Precision: Heuristic,
		})
	}
	return found
}

// within reports whether the definition at index is outer or nested in it.
// It follows parents at most once per definition, so cycles in hand-built
// analyses end.
func within(analysis symbols.Analysis, index, outer int) bool {
	for range len(analysis.Definitions) {
		if index == outer {
			return true
		}

		if index < 0 || index >= len(analysis.Definitions) {
			return false
		}
		index = analysis.Definitions[index].Parent
	}
	return false
}
