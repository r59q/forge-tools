package symbol

import (
	"context"
	"errors"
	"fmt"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// Change is one re-index of the code documents of a source, such as what a
// checkout sync reports.
type Change struct {
	// Source is the source of every document, such as a checkout ID.
	Source string `json:"source,omitempty"`

	// Documents are the added and replaced code documents, of kind
	// search.KindCode and of Source.
	Documents []search.Document `json:"documents"`

	// Removed are the paths of the documents that no longer exist.
	Removed []string `json:"removed"`
}

// UpdateResult reports what an update changed.
type UpdateResult struct {
	// Stored counts the documents whose symbols were written, and Symbols
	// the symbols they hold.
	Stored  int `json:"stored"`
	Symbols int `json:"symbols"`

	// Removed counts the documents whose symbols were dropped: removed
	// ones, and replaced ones that no longer give symbols.
	Removed int `json:"removed"`

	// Skipped counts the documents left out, which the index holds no
	// symbols for.
	Skipped Skipped `json:"skipped"`

	// SkippedPaths lists the documents counted in Skipped, in path order,
	// so that a caller can keep track of them. It is nil when none was.
	SkippedPaths []SkippedPath `json:"skippedPaths,omitempty"`
}

// SkippedPath is a document whose symbols were left out, and why.
type SkippedPath struct {
	Path   string     `json:"path"`
	Reason SkipReason `json:"reason"`
}

// Update brings the stored symbols of a source in an index in step with a
// change: it drops the symbols of the removed paths, then analyzes each
// changed document and replaces its symbols. Afterwards SearchIndex returns
// what Search returns for the documents of the source. The index must exist;
// stored symbols of other sources are not touched. Zero limits use
// DefaultUpdateLimits.
//
// Each document's symbols are one document of kind KindSymbol, with the
// path as ID and the code document's version, so a search sees the symbols
// of a document entirely old or entirely new. Documents that give no
// symbols, such as those left out and counted in UpdateResult.Skipped, hold
// none afterwards. Deleting the index deletes all stored symbols.
//
// A bad change returns search.ErrInvalid; more than Limits.Files documents
// to analyze or paths to remove, or more than Limits.TotalBytes of content,
// return search.ErrTooLarge, both before the index changes. An update that
// fails or is canceled returns what it changed so far; repeating it brings
// the index in step. Updates of one source in one index must not overlap.
func Update(
	ctx context.Context, analyzer Analyzer, engine *search.Engine, index string, change Change, limits Limits,
) (UpdateResult, error) {
	limits = limits.withDefaults(DefaultUpdateLimits())
	x := extractor{analyzer: analyzer, limits: limits}

	sorted, err := checkChange(change, limits)
	if err != nil {
		return UpdateResult{}, err
	}
	work, err := x.plan(sorted, Filter{})
	if err != nil {
		return UpdateResult{}, err
	}

	held, err := heldPaths(ctx, engine, index, change.Source)
	if err != nil {
		return UpdateResult{}, err
	}

	u := updater{engine: engine, index: index, source: change.Source, held: held}
	for _, path := range change.Removed {
		if err := u.drop(ctx, path); err != nil {
			return u.result, err
		}
	}
	for _, item := range work {
		if err := u.apply(ctx, x, item); err != nil {
			return u.result, err
		}
	}
	return u.result, nil
}

// updater applies one change to an index.
type updater struct {
	engine *search.Engine
	index  string
	source string
	held   map[string]bool
	result UpdateResult
}

// apply replaces the stored symbols of one changed document.
func (u *updater) apply(ctx context.Context, x extractor, item planned) error {
	path := item.document.ID
	if item.skipped != kept {
		u.skip(path, item.skipped)
		return u.drop(ctx, path)
	}

	extracted, err := x.extract(ctx, item.document, item.language)
	if err != nil {
		return err
	}
	u.skip(path, extracted.skipped)
	if len(extracted.symbols) == 0 {
		return u.drop(ctx, path)
	}

	stored := search.Document{Ref: storedRef(item.document.Ref), Content: extracted.stored}
	if err := u.engine.Put(ctx, u.index, stored); err != nil {
		return fmt.Errorf("store symbols: %w", err)
	}

	u.held[path] = true
	u.result.Stored++
	u.result.Symbols += len(extracted.symbols)
	return nil
}

// skip counts and lists a document left out for reason, if it was.
func (u *updater) skip(path string, reason skip) {
	if reason == kept {
		return
	}
	u.result.Skipped.add(reason)
	u.result.SkippedPaths = append(u.result.SkippedPaths, SkippedPath{Path: path, Reason: reason.reason()})
}

// drop removes the stored symbols of a path, if the index holds any.
func (u *updater) drop(ctx context.Context, path string) error {
	if !u.held[path] {
		return nil
	}

	ref := search.Ref{Kind: KindSymbol, Source: u.source, ID: path}
	if err := u.engine.Remove(ctx, u.index, ref); err != nil && !errors.Is(err, search.ErrNotFound) {
		return fmt.Errorf("remove symbols: %w", err)
	}

	delete(u.held, path)
	u.result.Removed++
	return nil
}

// heldPaths returns the paths of source that the index holds symbols for.
func heldPaths(ctx context.Context, engine *search.Engine, index, source string) (map[string]bool, error) {
	refs, err := engine.Documents(ctx, index, search.Filter{Kind: KindSymbol, Source: source})
	if err != nil {
		return nil, err
	}

	held := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref.Source == source {
			held[ref.ID] = true
		}
	}
	return held, nil
}

// checkChange checks a change and returns its documents in match order.
func checkChange(change Change, limits Limits) ([]*search.Document, error) {
	if !validName(change.Source) {
		return nil, fmt.Errorf("source: %w", search.ErrInvalid)
	}
	if len(change.Removed) > limits.Files {
		return nil, fmt.Errorf("removed paths: %w", search.ErrTooLarge)
	}

	sorted, err := ordered(change.Documents)
	if err != nil {
		return nil, err
	}

	paths := make(map[string]bool, len(sorted)+len(change.Removed))
	for _, document := range sorted {
		if document.Source != change.Source {
			return nil, fmt.Errorf("document source: %w", search.ErrInvalid)
		}
		paths[document.ID] = true
	}

	for index, path := range change.Removed {
		if path == "" || !validName(path) || paths[path] {
			return nil, fmt.Errorf("removed path %d: %w", index, search.ErrInvalid)
		}
		paths[path] = true
	}
	return sorted, nil
}
