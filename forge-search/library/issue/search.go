package issue

import (
	"context"
	"fmt"
	"math"

	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-search/library/search"
)

// Reader lists issues. It is implemented by issues.Client and remote
// adapters, and must return the complete listing or an error.
type Reader interface {
	List(context.Context, issues.ListOptions) ([]issues.Issue, error)
}

// Search searches the given issues unindexed and keeps nothing after it
// returns. Each issue is named by its own source ID, repository and number.
// Zero limits use DefaultLimits.
//
// Matches come in match order: by source ID, repository and issue number,
// then title before body, then by line. A bad query, filter or issue
// identity, or two issues with one identity, return search.ErrInvalid. More
// than Limits.Issues return search.ErrTooLarge. Issues over a size limit are
// left out and counted in Results.Issues.
//
// A state filter keeps the matches of issues in that state among the first
// Limits.Results matches of all states. When those were not all the
// matches, State.Truncated is set. A context that ends returns the matches
// found so far with State.Canceled.
func Search(ctx context.Context, list []issues.Issue, query search.Query, options Options) (Results, error) {
	limits := options.Limits.withDefaults(DefaultLimits())
	if err := checkSearch(ctx, query, options.Filter, limits); err != nil {
		return Results{}, err
	}

	own := func(item issues.Issue) home {
		return home{source: item.Source.ID, repository: item.Repository}
	}
	documents, left, err := mapIssues(list, own, options.Filter, limits)
	if err != nil {
		return Results{}, err
	}
	return unindexed(ctx, query, documents, left, options.Filter, limits)
}

// SearchRepository lists the open and closed issues of a repository through
// reader and searches them as Search does. Issues are named by
// repository.Source and repository.Repository, as Sync names them, so the
// results equal those of SearchIndex after a Sync of the same issues.
// repository.Source is required.
//
// A reader that fails returns its error; a context that ends while the
// reader lists returns no matches with State.Canceled.
func SearchRepository(
	ctx context.Context, reader Reader, repository issues.RepositoryRef, query search.Query, options Options,
) (Results, error) {
	limits := options.Limits.withDefaults(DefaultLimits())
	if err := checkSearch(ctx, query, options.Filter, limits); err != nil {
		return Results{}, err
	}
	if err := checkRepository(repository); err != nil {
		return Results{}, err
	}

	list, err := listIssues(ctx, reader, repository, limits)
	if ctx.Err() != nil {
		return canceled(), nil
	}
	if err != nil {
		return Results{}, err
	}

	at := homeOf(repository)
	documents, left, err := mapIssues(list, func(issues.Issue) home { return at }, options.Filter, limits)
	if err != nil {
		return Results{}, err
	}
	return unindexed(ctx, query, documents, left, options.Filter, limits)
}

// SearchIndex searches the issue documents of an index, as Sync left them.
// It returns what Search returns for the same issues, query, filter and
// result limit: the same matches, in the same order, with the same state.
// Only Limits.Results applies; it must not exceed the engine's result limit.
func SearchIndex(ctx context.Context, engine *search.Engine, index string, query search.Query, options Options) (Results, error) {
	limits := options.Limits.withDefaults(DefaultLimits())
	if err := checkSearch(ctx, query, options.Filter, limits); err != nil {
		return Results{}, err
	}

	filter := options.Filter
	return run(query, filter, limits, func(base search.Query) (search.Results, error) {
		return engine.Search(ctx, index, base, selection(filter))
	})
}

func unindexed(
	ctx context.Context, query search.Query, documents []search.Document, left State, filter Filter, limits Limits,
) (Results, error) {
	searchLimits := search.Limits{
		DocumentBytes: MaxTitleBytes + 1 + limits.BodyBytes,
		Documents:     max(len(documents), 1),
		TotalBytes:    math.MaxInt,
		Results:       limits.Results,
	}

	results, err := run(query, filter, limits, func(base search.Query) (search.Results, error) {
		return search.Unindexed(ctx, base, documents, searchLimits)
	})
	if err != nil {
		return Results{}, err
	}

	results.Issues = left
	return results, nil
}

// run searches with find, then keeps the matches of the filtered state. Both
// search modes use it, so they cut the same matches.
func run(query search.Query, filter Filter, limits Limits, find func(search.Query) (search.Results, error)) (Results, error) {
	wanted := query.Limit
	if wanted == 0 {
		wanted = limits.Results
	}

	// Both modes scan to the same limit, so that they stop at the same match
	// and count the same skipped documents.
	base := query
	base.Limit = wanted
	if filter.State != "" {
		base.Limit = limits.Results
	}

	found, err := find(base)
	if err != nil {
		return Results{}, err
	}
	return collect(found, filter.State, wanted)
}

func collect(found search.Results, state issues.State, limit int) (Results, error) {
	results := Results{Matches: []Match{}, State: found.State}

	for _, each := range found.Matches {
		match, err := toMatch(each)
		if err != nil {
			return Results{}, err
		}
		if state != "" && match.Issue.State != state {
			continue
		}
		if len(results.Matches) == limit {
			results.State.Truncated = true
			break
		}
		results.Matches = append(results.Matches, match)
	}
	return results, nil
}

// mapIssues returns the documents of the issues the filter selects, each in
// the home at gives it, and counts those left out.
func mapIssues(list []issues.Issue, at func(issues.Issue) home, filter Filter, limits Limits) ([]search.Document, State, error) {
	if len(list) > limits.Issues {
		return nil, State{}, fmt.Errorf("issues: %w", search.ErrTooLarge)
	}

	documents := []search.Document{}
	seen := make(map[home]map[int64]bool)
	var left State

	for index, item := range list {
		place := at(item)
		if !place.valid() {
			return nil, State{}, fmt.Errorf("issue %d: %w", index, search.ErrInvalid)
		}
		if !claim(seen, place, item.Number) {
			return nil, State{}, fmt.Errorf("duplicate issue %d: %w", item.Number, search.ErrInvalid)
		}
		if !selects(filter, place) {
			continue
		}

		document, fits, err := document(place, item, limits)
		if err != nil {
			return nil, State{}, err
		}
		if !fits {
			left.TooLarge++
			continue
		}
		documents = append(documents, document)
	}
	return documents, left, nil
}

// claim records an issue identity, and reports false when it was seen.
func claim(seen map[home]map[int64]bool, place home, number int64) bool {
	if seen[place] == nil {
		seen[place] = make(map[int64]bool)
	}
	if seen[place][number] {
		return false
	}
	seen[place][number] = true
	return true
}

func selects(filter Filter, place home) bool {
	return (filter.Source == "" || filter.Source == place.source) &&
		(filter.Repository == "" || filter.Repository == place.repository)
}

func listIssues(ctx context.Context, reader Reader, repository issues.RepositoryRef, limits Limits) ([]issues.Issue, error) {
	list, err := reader.List(ctx, issues.ListOptions{
		Source:     repository.Source,
		Repository: repository.Repository,
		State:      issues.StateAll,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("list issues: %w", err)
	}

	if len(list) > limits.Issues {
		return nil, fmt.Errorf("issues: %w", search.ErrTooLarge)
	}
	return list, nil
}

// checkSearch checks the filter and the query before any issue is read.
func checkSearch(ctx context.Context, query search.Query, filter Filter, limits Limits) error {
	if err := checkFilter(filter); err != nil {
		return err
	}
	_, err := search.Unindexed(ctx, query, nil, search.Limits{Results: limits.Results})
	return err
}

func checkRepository(repository issues.RepositoryRef) error {
	if repository.Source == "" || !homeOf(repository).valid() {
		return fmt.Errorf("repository: %w", search.ErrInvalid)
	}
	return nil
}

func homeOf(repository issues.RepositoryRef) home {
	return home{source: repository.Source, repository: repository.Repository}
}

func canceled() Results {
	return Results{Matches: []Match{}, State: search.State{Canceled: true}}
}
