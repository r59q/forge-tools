package symbol

import (
	"context"
	"strings"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// Search analyzes the given code documents and returns the symbols that
// match the query. It keeps nothing after it returns. Zero limits use
// DefaultLimits.
//
// Symbols come in match order: by document source and path, compared as
// bytes, then in the order of their declarations in the document. The first
// Query.Limit symbols in that order are returned.
//
// Documents in an unsupported language, binary documents and documents the
// analyzer cannot finish are left out and counted in Results.Skipped. A bad
// query, filter or document identity, a document that is not of kind
// search.KindCode, or two documents with one identity, return
// search.ErrInvalid. More than Limits.Files documents to analyze, or more
// than Limits.TotalBytes of their content, return search.ErrTooLarge before
// any is analyzed. A context that ends returns the symbols found so far with
// State.Canceled.
func Search(ctx context.Context, analyzer Analyzer, documents []search.Document, query Query, limits Limits) (Results, error) {
	limits = limits.withDefaults(DefaultLimits())

	query, err := checkQuery(ctx, query, limits)
	if err != nil {
		return Results{}, err
	}
	sorted, err := ordered(documents)
	if err != nil {
		return Results{}, err
	}

	x := extractor{analyzer: analyzer, limits: limits}
	work, err := x.plan(sorted, query.Filter)
	if err != nil {
		return Results{}, err
	}

	c := newCollector(query)
	for _, item := range work {
		if c.stopped(ctx) {
			break
		}
		if err := c.analyze(ctx, x, item); err != nil && !c.stopped(ctx) {
			return Results{}, err
		}
	}
	return c.result, nil
}

// SearchIndex searches the symbols Update stored in an index. It returns
// what Search returns for the same documents, query and result limit: the
// same symbols, in the same order, with the same state. Only Limits.Results
// applies. Results.Skipped stays zero; Update reports it instead.
//
// The index narrows the search to stored documents with names that hold
// every three-byte sequence of the query text. A context that ends returns
// the symbols found so far with State.Canceled.
func SearchIndex(ctx context.Context, engine *search.Engine, index string, query Query, limits Limits) (Results, error) {
	limits = limits.withDefaults(DefaultLimits())

	query, err := checkQuery(ctx, query, limits)
	if err != nil {
		return Results{}, err
	}

	c := newCollector(query)
	if c.stopped(ctx) {
		return c.result, nil
	}

	var failure error
	err = engine.Read(ctx, index, query.Text, storedFilter(query.Filter), func(document search.Document) bool {
		if !c.stopped(ctx) {
			failure = c.stored(document)
		}
		return failure == nil && !c.done
	})
	if err == nil {
		err = failure
	}
	if err != nil && !c.stopped(ctx) {
		return Results{}, err
	}
	return c.result, nil
}

// collector gathers the symbols of one search in match order.
type collector struct {
	query  Query
	done   bool
	result Results
}

func newCollector(query Query) *collector {
	return &collector{query: query, result: Results{Symbols: []Symbol{}}}
}

// stopped reports whether the search must end, and records why.
func (c *collector) stopped(ctx context.Context) bool {
	if ctx.Err() != nil {
		c.result.State.Canceled = true
		c.done = true
	}
	return c.done
}

// analyze adds the matching symbols of one planned document.
func (c *collector) analyze(ctx context.Context, x extractor, item planned) error {
	if item.skipped != kept {
		c.result.Skipped.add(item.skipped)
		return nil
	}

	extracted, err := x.extract(ctx, item.document, item.language)
	if err != nil {
		return err
	}
	c.result.Skipped.add(extracted.skipped)

	for _, symbol := range extracted.symbols {
		if c.named(symbol.Name) {
			c.offer(symbol)
		}
		if c.done {
			break
		}
	}
	return nil
}

// stored adds the matching symbols of one stored document.
func (c *collector) stored(document search.Document) error {
	var failure error

	err := eachName(document.Content, func(name string, data []byte) bool {
		if !c.named(name) {
			return true
		}

		symbol, err := decode(document.Ref, name, data)
		if err != nil {
			failure = err
			return false
		}
		c.offer(symbol)
		return !c.done
	})
	if err != nil {
		return err
	}
	return failure
}

// named reports whether a name matches the query text.
func (c *collector) named(name string) bool {
	if c.query.Exact {
		return name == c.query.Text
	}
	return strings.Contains(name, c.query.Text)
}

// offer adds a symbol the kind and language filters select, or ends the
// search when the result limit is full.
func (c *collector) offer(symbol Symbol) {
	filter := c.query.Filter
	selected := (filter.Kind == "" || filter.Kind == symbol.Kind) &&
		(filter.Language == "" || filter.Language == symbol.Language)
	if !selected {
		return
	}

	if len(c.result.Symbols) >= c.query.Limit {
		c.result.State.Truncated = true
		c.done = true
		return
	}
	c.result.Symbols = append(c.result.Symbols, symbol)
}

// storedFilter returns the index filter of the stored documents a filter
// selects.
func storedFilter(filter Filter) search.Filter {
	return search.Filter{Kind: KindSymbol, Source: filter.Source, ID: filter.Path, IDPrefix: filter.PathPrefix}
}
