package navigation

import (
	"slices"

	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// DefinitionsResult lists the definitions of a name, ranked.
type DefinitionsResult struct {
	// Name is the name the target resolved to.
	Name        string             `json:"name"`
	Definitions []DefinitionResult `json:"definitions"`

	// Truncated reports that Limits.Results cut the list.
	Truncated bool `json:"truncated"`
}

// Definitions finds the definitions named like the target across the files,
// of Target.Kind when set, and ranks them: same file, then same container,
// then the rest. Same-named definitions, such as overloads or methods of
// different types, are all returned.
//
// It returns symbols.ErrTooLarge for a request over the limits,
// ErrInvalidTarget and ErrNotFound for a target that does not resolve.
func Definitions(files []symbols.Analysis, target Target, limits Limits) (DefinitionsResult, error) {
	resolved, limits, err := prepare(files, target, limits)
	if err != nil {
		return DefinitionsResult{}, err
	}

	found := resolved.definitions(files)
	remaining := budget{left: limits.Results}
	kept := remaining.take(len(found))

	result := DefinitionsResult{Name: resolved.name, Truncated: remaining.truncated}
	for _, candidate := range found[:kept] {
		result.Definitions = append(result.Definitions, candidate.result)
	}
	return result, nil
}

// candidate is a ranked definition and where it is.
type candidate struct {
	location
	result DefinitionResult
	rank   rank
}

// definitions returns the definitions named like the anchor, ranked.
func (a anchor) definitions(files []symbols.Analysis) []candidate {
	var found []candidate

	for file, analysis := range files {
		for index, definition := range analysis.Definitions {
			kinded := a.kind == "" || definition.Kind == a.kind
			if definition.Name == a.name && kinded {
				found = append(found, a.candidate(analysis, file, index))
			}
		}
	}

	slices.SortFunc(found, func(x, y candidate) int {
		return compareRanks(x.rank, y.rank)
	})
	return found
}

func (a anchor) candidate(analysis symbols.Analysis, file, index int) candidate {
	definition := analysis.Definitions[index]
	tier := a.tier(analysis.Path, definition.Container)

	return candidate{
		location: location{file: file, index: index},
		result: DefinitionResult{
			Path:       analysis.Path,
			Index:      index,
			Definition: definition,
			Tier:       tier,
			Precision:  Heuristic,
		},
		rank: rank{tier: tier, path: analysis.Path, at: start(definition.NameRange), file: file, index: index},
	}
}
