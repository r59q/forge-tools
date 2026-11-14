package search

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// validateQuery checks the query and returns it with its default limit.
func validateQuery(query Query, limits Limits) (Query, error) {
	text := query.Text

	validText := text != "" && len(text) <= MaxQueryBytes &&
		utf8.ValidString(text) && !strings.ContainsAny(text, "\x00\r\n")
	if !validText {
		return Query{}, fmt.Errorf("query text: %w", ErrInvalid)
	}

	if query.Limit < 0 || query.Limit > limits.Results {
		return Query{}, fmt.Errorf("query limit: %w", ErrInvalid)
	}
	if query.Limit == 0 {
		query.Limit = limits.Results
	}
	return query, nil
}

// ordered checks the documents and returns them in match order. The caller's
// slice is not changed. Errors name the index of a document, never its content.
func ordered(documents []Document, limits Limits) ([]*Document, error) {
	if len(documents) > limits.Documents {
		return nil, fmt.Errorf("documents: %w", ErrTooLarge)
	}

	sorted := make([]*Document, len(documents))
	for index := range documents {
		document := &documents[index]

		if !validRef(document.Ref) {
			return nil, fmt.Errorf("document %d: %w", index, ErrInvalid)
		}
		if len(document.Content) > limits.DocumentBytes {
			return nil, fmt.Errorf("document %d: %w", index, ErrTooLarge)
		}
		sorted[index] = document
	}

	slices.SortFunc(sorted, func(a, b *Document) int { return compareRefs(a.Ref, b.Ref) })

	for index := 1; index < len(sorted); index++ {
		if compareRefs(sorted[index-1].Ref, sorted[index].Ref) == 0 {
			return nil, fmt.Errorf("duplicate document: %w", ErrInvalid)
		}
	}
	return sorted, nil
}

// compareRefs orders documents by kind, source and ID, comparing bytes.
// Version is not part of the identity.
func compareRefs(a, b Ref) int {
	return cmp.Or(
		strings.Compare(string(a.Kind), string(b.Kind)),
		strings.Compare(a.Source, b.Source),
		strings.Compare(a.ID, b.ID),
	)
}

func validRef(ref Ref) bool {
	required := ref.Kind != "" && ref.ID != ""

	return required &&
		validName(string(ref.Kind)) &&
		validName(ref.Source) &&
		validName(ref.ID) &&
		validName(ref.Version)
}

func validName(name string) bool {
	return len(name) <= MaxNameBytes && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}
