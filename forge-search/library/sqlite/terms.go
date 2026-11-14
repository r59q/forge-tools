package sqlite

import (
	"encoding/binary"
	"math"
	"strconv"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// encodeTerms packs sorted, distinct terms as the varint gaps between them.
// A document keeps its terms in this form, so that a Put or Remove finds the
// rows to delete without a second index over the terms table.
func encodeTerms(terms []search.Term) []byte {
	encoded := make([]byte, 0, 2*len(terms))
	var previous search.Term

	for _, term := range terms {
		encoded = binary.AppendUvarint(encoded, uint64(term-previous))
		previous = term
	}
	return encoded
}

// decodeTerms reverses encodeTerms. Malformed data returns ErrStorage.
func decodeTerms(encoded []byte) ([]search.Term, error) {
	var (
		terms    []search.Term
		previous uint64
	)
	for len(encoded) > 0 {
		gap, size := binary.Uvarint(encoded)
		if size <= 0 || previous+gap > math.MaxUint32 {
			return nil, ErrStorage
		}
		encoded = encoded[size:]

		previous += gap
		terms = append(terms, search.Term(previous))
	}
	return terms, nil
}

// diffTerms returns the terms of now that were not in before, and the terms
// of before that are not in now. Both slices are sorted and distinct.
func diffTerms(before, now []search.Term) (added, removed []search.Term) {
	for len(before) > 0 || len(now) > 0 {
		switch {
		case len(now) == 0 || len(before) > 0 && before[0] < now[0]:
			removed = append(removed, before[0])
			before = before[1:]
		case len(before) == 0 || now[0] < before[0]:
			added = append(added, now[0])
			now = now[1:]
		default:
			before, now = before[1:], now[1:]
		}
	}
	return added, removed
}

// jsonTerms returns terms as a JSON array, for json_each.
func jsonTerms(terms []search.Term) string {
	text := make([]byte, 0, 2+9*len(terms))
	text = append(text, '[')

	for position, term := range terms {
		if position > 0 {
			text = append(text, ',')
		}
		text = strconv.AppendUint(text, uint64(term), 10)
	}
	return string(append(text, ']'))
}
