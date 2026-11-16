package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-search/library/symbol"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// DefaultSymbolParses is the default for Config.SymbolParses.
const DefaultSymbolParses = 2

// boundedAnalyzer runs at most cap(slots) analyses at once, across every
// call of a server. Others wait for a slot until their context ends.
type boundedAnalyzer struct {
	analyzer symbol.Analyzer
	slots    chan struct{}
}

func newBoundedAnalyzer(analyzer symbol.Analyzer, parses int) *boundedAnalyzer {
	return &boundedAnalyzer{analyzer: analyzer, slots: make(chan struct{}, parses)}
}

// Analyze analyzes a file once a slot is free.
func (b *boundedAnalyzer) Analyze(ctx context.Context, file symbols.File) (symbols.Analysis, error) {
	select {
	case b.slots <- struct{}{}:
	case <-ctx.Done():
		return symbols.Analysis{}, ctx.Err()
	}
	defer func() { <-b.slots }()

	return b.analyzer.Analyze(ctx, file)
}
