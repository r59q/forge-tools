package navigation

import (
	"slices"

	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// IncomingResult lists the references to a name, grouped by the
// definition around them and ranked.
type IncomingResult struct {
	// Name is the name the target resolved to.
	Name   string          `json:"name"`
	Groups []IncomingGroup `json:"groups"`

	// Truncated reports that Limits.Results cut the groups or references.
	Truncated bool `json:"truncated"`
}

// IncomingGroup is one caller and its references to the target, in source
// order. References outside any definition share one group per file whose
// Caller has Index symbols.None.
type IncomingGroup struct {
	Caller     DefinitionResult  `json:"caller"`
	References []ReferenceResult `json:"references"`
}

// Incoming finds the references named like the target across the files, of
// any kind, and groups them by the innermost definition around each one.
// Groups are ranked by their caller: same file, then same container, then
// the rest, then by the position of their first reference.
//
// References to every definition of the name are returned together, since
// overloads and same-named methods of different types cannot be told apart.
//
// It returns the errors of Definitions.
func Incoming(files []symbols.Analysis, target Target, limits Limits) (IncomingResult, error) {
	resolved, limits, err := prepare(files, target, limits)
	if err != nil {
		return IncomingResult{}, err
	}

	result := IncomingResult{Name: resolved.name}
	remaining := budget{left: limits.Results}

	for _, group := range resolved.callers(files) {
		kept, ok := remaining.group(len(group.References))
		if !ok {
			break
		}
		group.References = group.References[:kept]
		result.Groups = append(result.Groups, group)
	}

	result.Truncated = remaining.truncated
	return result, nil
}

// ranked is a group with its rank.
type ranked struct {
	group IncomingGroup
	rank  rank
}

// callers returns the groups of references named like the anchor, ranked.
func (a anchor) callers(files []symbols.Analysis) []IncomingGroup {
	var groups []ranked

	for file, analysis := range files {
		groups = append(groups, a.callersIn(analysis, file)...)
	}

	slices.SortFunc(groups, func(x, y ranked) int {
		return compareRanks(x.rank, y.rank)
	})

	sorted := make([]IncomingGroup, 0, len(groups))
	for _, group := range groups {
		sorted = append(sorted, group.group)
	}
	return sorted
}

// callersIn groups the matching references of one file in the order their
// first reference appears.
func (a anchor) callersIn(analysis symbols.Analysis, file int) []ranked {
	var groups []ranked
	byCaller := map[int]int{}

	for index, reference := range analysis.References {
		if reference.Name != a.name {
			continue
		}

		caller := callerIndex(analysis, reference)
		at, ok := byCaller[caller]
		if !ok {
			at = len(groups)
			byCaller[caller] = at
			groups = append(groups, a.group(analysis, file, caller, reference))
		}

		groups[at].group.References = append(groups[at].group.References, ReferenceResult{
			Path:      analysis.Path,
			Index:     index,
			Reference: reference,
			Precision: Heuristic,
		})
	}
	return groups
}

// callerIndex returns the definition around the reference, or symbols.None
// at the file level or for an index out of range.
func callerIndex(analysis symbols.Analysis, reference symbols.Reference) int {
	if _, ok := analysis.Enclosing(reference); !ok {
		return symbols.None
	}
	return reference.Enclosing
}

// group starts the group of a caller at its first reference.
func (a anchor) group(analysis symbols.Analysis, file, caller int, first symbols.Reference) ranked {
	definition, _ := analysis.Enclosing(first)
	tier := a.tier(analysis.Path, definition.Container)

	return ranked{
		group: IncomingGroup{
			Caller: DefinitionResult{
				Path:       analysis.Path,
				Index:      caller,
				Definition: definition,
				Tier:       tier,
				Precision:  Heuristic,
			},
		},
		rank: rank{tier: tier, path: analysis.Path, at: start(first.Range), file: file, index: caller},
	}
}
