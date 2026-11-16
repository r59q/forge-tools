package search

import (
	"context"
	"fmt"
	"unicode/utf8"
)

// Read calls visit with each document of an index that the filter selects
// and that may hold text, in match order, until visit returns false. A
// document may hold text when it holds every three-byte sequence of it, so
// text of fewer than three bytes, or none, selects every document the filter
// selects. Each document is read entirely as it was before an update or
// entirely as it is after.
//
// Read lets other aggregates, such as symbols, keep records in documents of
// their own kind and match them by their own rules, narrowed by the index.
// Text is valid UTF-8 of at most MaxQueryBytes. The content is only valid
// during the call and must not be changed. Binary documents are visited
// without content.
func (e *Engine) Read(ctx context.Context, index, text string, filter Filter, visit func(Document) bool) error {
	if err := checkSelection(index, filter); err != nil {
		return err
	}
	if len(text) > MaxQueryBytes || !utf8.ValidString(text) {
		return fmt.Errorf("read text: %w", ErrInvalid)
	}

	selection := Selection{Filter: filter, Terms: contentTerms([]byte(text))}
	return e.store.Documents(ctx, index, selection, visit)
}
