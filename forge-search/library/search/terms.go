package search

import "slices"

// termBytes is the length of the byte sequences that become terms. A query
// shorter than this has no terms and is answered by scanning.
const termBytes = 3

// unsearchableTerm marks an entry whose content was not stored because it is
// binary. It is outside the range of content terms.
const unsearchableTerm Term = 1 << (8 * termBytes)

// contentTerms returns the sorted, distinct terms of text: one for every
// three consecutive bytes of a line. A document can only hold a query when it
// holds every term of that query.
func contentTerms(text []byte) []Term {
	if len(text) < termBytes {
		return nil
	}
	terms := make([]Term, 0, len(text)-termBytes+1)

	var window Term
	run := 0
	for _, value := range text {
		if value == '\n' || value == '\r' {
			run = 0
			continue
		}
		window = (window<<8 | Term(value)) & (unsearchableTerm - 1)
		if run++; run >= termBytes {
			terms = append(terms, window)
		}
	}

	slices.Sort(terms)
	return slices.Compact(terms)
}

// newEntry returns what the index stores for a document. Binary content is
// not stored: the entry only records that the document exists.
func newEntry(document Document) Entry {
	if !searchable(document.Content) {
		return Entry{Document: Document{Ref: document.Ref}, Terms: []Term{unsearchableTerm}}
	}
	return Entry{Document: document, Terms: contentTerms(document.Content)}
}
