package sqlite

import (
	"encoding/binary"
	"math"
	"strconv"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// encodeTerms packs sorted, distinct terms as the varint gaps between them.
// A document keeps its terms in this form, so that a Put or Remove can give
// the full-text index the tokens it deletes.
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

// tokens returns the terms of an index as the text of the full-text index:
// one token per term, of the index ID and term in base 32 around an x, so
// that each index has tokens of its own. Space separates the tokens, and a
// full-text query of them matches the documents that hold all of them.
func tokens(index int64, terms []search.Term) string {
	prefix := strconv.AppendInt(nil, index, 32)
	prefix = append(prefix, 'x')

	text := make([]byte, 0, len(terms)*(len(prefix)+6))
	for position, term := range terms {
		if position > 0 {
			text = append(text, ' ')
		}
		text = append(text, prefix...)
		text = strconv.AppendUint(text, uint64(term), 32)
	}
	return string(text)
}
