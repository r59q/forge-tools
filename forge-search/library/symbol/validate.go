package symbol

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// checkQuery checks the query and filter, and returns the query with its
// default limit.
func checkQuery(ctx context.Context, query Query, limits Limits) (Query, error) {
	base := search.Query{Text: query.Text, Limit: query.Limit}
	if _, err := search.Unindexed(ctx, base, nil, search.Limits{Results: limits.Results}); err != nil {
		return Query{}, err
	}

	filter := query.Filter
	validFilter := validName(filter.Source) && validName(filter.Path) && validName(filter.PathPrefix) &&
		validName(filter.Kind) && validName(filter.Language)
	if !validFilter {
		return Query{}, fmt.Errorf("symbol filter: %w", search.ErrInvalid)
	}

	if query.Limit == 0 {
		query.Limit = limits.Results
	}
	return query, nil
}

// ordered checks code documents and returns them in match order. The
// caller's slice is not changed. Errors name the index of a document, never
// its content.
func ordered(documents []search.Document) ([]*search.Document, error) {
	sorted := make([]*search.Document, len(documents))

	for index := range documents {
		document := &documents[index]
		if !validDocument(document.Ref) {
			return nil, fmt.Errorf("document %d: %w", index, search.ErrInvalid)
		}
		sorted[index] = document
	}

	slices.SortFunc(sorted, func(a, b *search.Document) int { return compareRefs(a.Ref, b.Ref) })

	for index := 1; index < len(sorted); index++ {
		if compareRefs(sorted[index-1].Ref, sorted[index].Ref) == 0 {
			return nil, fmt.Errorf("duplicate document: %w", search.ErrInvalid)
		}
	}
	return sorted, nil
}

// planned is one document of a call, with its language or the reason it is
// not analyzed.
type planned struct {
	document *search.Document
	language string
	skipped  skip
}

// plan returns the sorted documents the filter selects, classified, and
// checks that the call may analyze them all.
func (x extractor) plan(sorted []*search.Document, filter Filter) ([]planned, error) {
	work := []planned{}
	files, bytes := 0, 0

	for _, document := range sorted {
		if !selects(filter, document.Ref) {
			continue
		}

		language, skipped := x.classify(document)
		if skipped == kept && filter.Language != "" && filter.Language != language {
			continue
		}
		if skipped == kept {
			files++
			bytes += len(document.Content)
		}
		work = append(work, planned{document: document, language: language, skipped: skipped})
	}

	if err := x.checkSize(files, bytes); err != nil {
		return nil, err
	}
	return work, nil
}

func (x extractor) checkSize(files, bytes int) error {
	if files > x.limits.Files || bytes > x.limits.TotalBytes {
		return fmt.Errorf("documents: %w", search.ErrTooLarge)
	}
	return nil
}

// selects reports whether the source and path fields of a filter select a
// code document.
func selects(filter Filter, ref search.Ref) bool {
	return (filter.Source == "" || filter.Source == ref.Source) &&
		(filter.Path == "" || filter.Path == ref.ID) &&
		strings.HasPrefix(ref.ID, filter.PathPrefix)
}

func validDocument(ref search.Ref) bool {
	return ref.Kind == search.KindCode && ref.ID != "" &&
		validName(ref.Source) && validName(ref.ID) && validName(ref.Version)
}

// compareRefs orders code documents by source and path, comparing bytes.
func compareRefs(a, b search.Ref) int {
	return cmp.Or(strings.Compare(a.Source, b.Source), strings.Compare(a.ID, b.ID))
}

func validName(name string) bool {
	return len(name) <= search.MaxNameBytes && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}
