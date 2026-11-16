package symbol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// skip says why a document gives no symbols.
type skip int

const (
	kept skip = iota
	unsupported
	tooLarge
	failed
)

func (s *Skipped) add(reason skip) {
	switch reason {
	case unsupported:
		s.Unsupported++
	case tooLarge:
		s.TooLarge++
	case failed:
		s.Failed++
	}
}

// extraction is what one document gives: its symbols in source order, and
// their stored form.
type extraction struct {
	symbols []Symbol
	stored  []byte
	skipped skip
}

// extractor turns code documents into symbols. Both search modes use it, so
// that they see the same symbols.
type extractor struct {
	analyzer Analyzer
	limits   Limits
}

// classify returns the language of a document, or why it is not analyzed.
func (x extractor) classify(document *search.Document) (string, skip) {
	language, known := symbols.DetectLanguage(document.ID)
	binary := !utf8.Valid(document.Content) || bytes.IndexByte(document.Content, 0) >= 0

	switch {
	case !known || binary:
		return "", unsupported
	case len(document.Content) > x.limits.FileBytes:
		return "", tooLarge
	}
	return language, kept
}

// extract analyzes one document of a language classify returned. Only
// unexpected analyzer errors and the end of ctx are errors.
func (x extractor) extract(ctx context.Context, document *search.Document, language string) (extraction, error) {
	file := symbols.File{Path: document.ID, Content: document.Content, Language: language}

	analysis, err := x.analyzer.Analyze(ctx, file)
	if err != nil {
		return failure(ctx, err)
	}
	if len(analysis.Definitions) > x.limits.Symbols {
		return extraction{skipped: tooLarge}, nil
	}

	lines := lineStarts(document.Content)
	found := make([]Symbol, 0, len(analysis.Definitions))

	for _, definition := range analysis.Definitions {
		if symbol, ok := toSymbol(document, lines, language, definition); ok {
			found = append(found, symbol)
		}
	}

	stored := encode(found)
	if len(stored) > MaxRecordBytes {
		return extraction{skipped: tooLarge}, nil
	}
	return extraction{symbols: found, stored: stored}, nil
}

// failure sorts an analyzer error into a reason to skip the document, or an
// error for the call.
func failure(ctx context.Context, err error) (extraction, error) {
	switch {
	case ctx.Err() != nil:
		return extraction{}, ctx.Err()
	case errors.Is(err, symbols.ErrUnsupported):
		return extraction{skipped: unsupported}, nil
	case errors.Is(err, symbols.ErrTooLarge):
		return extraction{skipped: tooLarge}, nil
	case errors.Is(err, symbols.ErrTimeout), errors.Is(err, symbols.ErrMemory):
		return extraction{skipped: failed}, nil
	}
	return extraction{}, fmt.Errorf("analyze symbols: %w", err)
}

// toSymbol returns the symbol of a definition, or false when its name,
// kind or container is not one an index can hold.
func toSymbol(document *search.Document, lines []int, language string, definition symbols.Definition) (Symbol, bool) {
	if !validSymbol(definition) {
		return Symbol{}, false
	}

	preview, column, ok := declaration(document.Content, lines, definition)
	if !ok {
		return Symbol{}, false
	}

	return Symbol{
		Document:      search.Ref{Kind: search.KindCode, Source: document.Source, ID: document.ID, Version: document.Version},
		Name:          definition.Name,
		Kind:          definition.Kind,
		Language:      language,
		NameRange:     toRange(definition.NameRange),
		Range:         toRange(definition.Range),
		Container:     definition.Container,
		Preview:       preview,
		PreviewColumn: column,
	}, true
}

func validSymbol(definition symbols.Definition) bool {
	name := definition.Name
	validName := name != "" && !strings.ContainsAny(name, "\x00\t\r\n")

	return validName && validText(name) && validText(definition.Kind) && validText(definition.Container)
}

func validText(text string) bool {
	return len(text) <= MaxNameBytes && utf8.ValidString(text)
}

func toRange(r symbols.Range) search.Range {
	return search.Range{StartLine: r.StartLine, StartColumn: r.StartColumn, EndLine: r.EndLine, EndColumn: r.EndColumn}
}
