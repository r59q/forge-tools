package symbol

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// record is a stored symbol without its name and document. A stored document
// holds one line per symbol, in source order: the name, a tab and the
// record as JSON. The names are plain text, so the index narrows a search by
// them; JSON escapes every tab and line break in the record.
type record struct {
	Kind          string `json:"k"`
	Language      string `json:"l"`
	NameRange     [4]int `json:"n"`
	Range         [4]int `json:"r"`
	Container     string `json:"c,omitempty"`
	Preview       string `json:"p"`
	PreviewColumn int    `json:"pc"`
}

// encode returns the stored form of the symbols of one document.
func encode(found []Symbol) []byte {
	var buffer bytes.Buffer

	for _, symbol := range found {
		// A record of valid strings and numbers always encodes.
		data, _ := json.Marshal(record{
			Kind:          symbol.Kind,
			Language:      symbol.Language,
			NameRange:     fromRange(symbol.NameRange),
			Range:         fromRange(symbol.Range),
			Container:     symbol.Container,
			Preview:       symbol.Preview,
			PreviewColumn: symbol.PreviewColumn,
		})

		buffer.WriteString(symbol.Name)
		buffer.WriteByte('\t')
		buffer.Write(data)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes()
}

// storedRef returns the identity of the stored symbols of a code document.
func storedRef(ref search.Ref) search.Ref {
	return search.Ref{Kind: KindSymbol, Source: ref.Source, ID: ref.ID, Version: ref.Version}
}

// eachName calls visit with the name and record of each stored symbol, in
// order, until visit returns false.
func eachName(content []byte, visit func(name string, data []byte) bool) error {
	for len(content) > 0 {
		line, rest, _ := bytes.Cut(content, []byte("\n"))
		content = rest

		name, data, found := bytes.Cut(line, []byte("\t"))
		if !found || len(name) == 0 {
			return fmt.Errorf("stored symbols: %w", search.ErrInvalid)
		}
		if !visit(string(name), data) {
			return nil
		}
	}
	return nil
}

// decode returns the stored symbol named name in the stored document ref.
func decode(ref search.Ref, name string, data []byte) (Symbol, error) {
	var stored record
	if err := json.Unmarshal(data, &stored); err != nil {
		return Symbol{}, fmt.Errorf("stored symbols: %w", search.ErrInvalid)
	}

	return Symbol{
		Document:      search.Ref{Kind: search.KindCode, Source: ref.Source, ID: ref.ID, Version: ref.Version},
		Name:          name,
		Kind:          stored.Kind,
		Language:      stored.Language,
		NameRange:     toSearchRange(stored.NameRange),
		Range:         toSearchRange(stored.Range),
		Container:     stored.Container,
		Preview:       stored.Preview,
		PreviewColumn: stored.PreviewColumn,
	}, nil
}

func fromRange(r search.Range) [4]int {
	return [4]int{r.StartLine, r.StartColumn, r.EndLine, r.EndColumn}
}

func toSearchRange(r [4]int) search.Range {
	return search.Range{StartLine: r[0], StartColumn: r[1], EndLine: r[2], EndColumn: r[3]}
}
