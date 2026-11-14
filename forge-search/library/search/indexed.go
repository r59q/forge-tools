package search

import (
	"context"
	"math"
	"slices"
)

// Search finds the query text in the documents of an index that the filter
// selects. It follows the rules of Unindexed and returns what Unindexed
// returns for the same documents, query and result limit: the same matches,
// in the same order, with the same state.
//
// The index narrows the search to documents that hold every three-byte
// sequence of the query, then checks each of them with the matcher Unindexed
// uses. A query of one or two bytes cannot be narrowed, so every selected
// document is checked: slower, never incomplete.
//
// There is no byte budget: the size of the index bounds the work, and
// State.Truncated only reports the result limit. A context that ends returns
// the matches found so far with State.Canceled.
func (e *Engine) Search(ctx context.Context, index string, query Query, filter Filter) (Results, error) {
	if err := checkSelection(index, filter); err != nil {
		return Results{}, err
	}
	query, err := validateQuery(query, Limits{Results: e.limits.Results})
	if err != nil {
		return Results{}, err
	}

	s := scan{
		query:  []byte(query.Text),
		limit:  query.Limit,
		budget: math.MaxInt,
		result: Results{Matches: []Match{}},
	}
	if s.stopped(ctx) {
		return s.result, nil
	}

	if err := e.verify(ctx, &s, index, filter); err != nil && !s.stopped(ctx) {
		return Results{}, err
	}
	return s.result, nil
}

// verify scans the documents that may hold the query, in match order, and
// counts the binary documents the scan passed.
func (e *Engine) verify(ctx context.Context, s *scan, index string, filter Filter) error {
	binary, err := e.refs(ctx, index, Selection{Filter: filter, Terms: []Term{unsearchableTerm}})
	if err != nil {
		return err
	}

	var reached *Ref
	selection := Selection{Filter: filter, Terms: contentTerms(s.query)}

	err = e.store.Documents(ctx, index, selection, func(document Document) bool {
		reached = &document.Ref
		if !s.stopped(ctx) {
			s.document(ctx, &document)
		}
		return !s.done
	})

	if err == nil && !s.done {
		reached = nil
	}
	s.result.State.Skipped = passed(binary, reached, err == nil)
	return err
}

// passed counts the documents of sorted that come before the one the scan
// stopped at. A scan that reached the end passed all of them.
func passed(sorted []Ref, stop *Ref, complete bool) int {
	switch {
	case stop != nil:
		before, _ := slices.BinarySearchFunc(sorted, *stop, compareRefs)
		return before
	case complete:
		return len(sorted)
	default:
		return 0
	}
}
