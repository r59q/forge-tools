package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"

	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-search/library/symbol"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// KindSymbolRecord is the kind of the document that records a complete
// symbol pass: its ID is the revision whose definitions the index holds. It
// has no content, and searches never match it.
const KindSymbolRecord search.Kind = "symbol-index"

// KindSymbolSkipped is the kind of the documents that record the code files
// in a language with symbol support that a symbol pass left out: the ID is
// the path and the version the symbol.SkipReason. They have no content, and
// searches never match them.
const KindSymbolSkipped search.Kind = "symbol-skipped"

// Bounds of the symbol pass of one indexing: the files in a language with
// symbol support it analyzes, and their content. A pass that would pass
// them stops, and leaves the symbols of the index incomplete.
const (
	SymbolPassFiles = 10000
	SymbolPassBytes = 256 << 20
)

// Bounds of one symbol.Update call of a pass.
const (
	chunkFiles = 500
	chunkBytes = 16 << 20
)

// emptyVersion is the version of an empty code document. A code document
// without content and with another version is binary.
var emptyVersion = func() string { //nolint:gochecknoglobals // A constant the compiler cannot compute.
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}()

// symbolPass brings the definitions of an index in step with its code.
type symbolPass struct {
	engine   *search.Engine
	analyzer symbol.Analyzer
	index    string

	// skipped holds the reason of each skip document of the index.
	skipped map[string]symbol.SkipReason

	// files and bytes count what the pass analyzed.
	files, bytes int

	result symbol.UpdateResult
	full   bool
	stop   bool
}

// clearSymbols removes the symbol record of an index, before its code
// changes, and reports whether the record named the revision of the code:
// whether a symbol pass may follow the change set alone.
func (s *Server) clearSymbols(ctx context.Context, index string) (bool, error) {
	engine := s.config.Engine

	info, err := engine.Index(ctx, index)
	if err != nil {
		return false, err
	}
	records, err := engine.Documents(ctx, index, search.Filter{Kind: KindSymbolRecord})
	if err != nil {
		return false, err
	}

	current := false
	for _, record := range records {
		current = current || (info.Marker != "" && record.ID == info.Marker)
		if err := engine.Remove(ctx, index, record); err != nil && !errors.Is(err, search.ErrNotFound) {
			return false, err
		}
	}
	return current, nil
}

// indexSymbols runs the symbol pass after a code sync: of the changed and
// removed paths when incremental, of every code file otherwise. It records
// revision unless the pass stopped at a limit.
func (s *Server) indexSymbols(
	ctx context.Context, index, revision string, changed, removed []string, incremental bool,
) (*pb.SymbolPass, error) {
	p := &symbolPass{engine: s.config.Engine, analyzer: s.analyzer, index: index, full: !incremental}

	skipped, err := p.engine.Documents(ctx, index, search.Filter{Kind: KindSymbolSkipped})
	if err != nil {
		return nil, err
	}
	p.skipped = make(map[string]symbol.SkipReason, len(skipped))
	for _, ref := range skipped {
		p.skipped[ref.ID] = symbol.SkipReason(ref.Version)
	}

	if !incremental {
		if changed, removed, err = p.everything(ctx); err != nil {
			return nil, err
		}
	}
	if err := p.remove(ctx, removed); err != nil {
		return nil, err
	}
	if err := p.update(ctx, changed); err != nil {
		return nil, err
	}

	if !p.stop && revision != "" {
		ref := search.Ref{Kind: KindSymbolRecord, ID: revision, Version: "1"}
		if err := p.engine.Put(ctx, index, search.Document{Ref: ref, Content: []byte{}}); err != nil && !p.halt(err) {
			return nil, err
		}
	}
	return p.proto(), nil
}

// everything returns every code path of the index as changed, and the paths
// with definitions or a skip record but no code as removed.
func (p *symbolPass) everything(ctx context.Context) (changed, removed []string, err error) {
	code, err := p.engine.Documents(ctx, p.index, search.Filter{Kind: search.KindCode})
	if err != nil {
		return nil, nil, err
	}
	held, err := p.engine.Documents(ctx, p.index, search.Filter{Kind: symbol.KindSymbol})
	if err != nil {
		return nil, nil, err
	}

	paths := map[string]bool{}
	for _, ref := range code {
		changed = append(changed, ref.ID)
		paths[ref.ID] = true
	}

	gone := map[string]bool{}
	for _, ref := range held {
		gone[ref.ID] = !paths[ref.ID]
	}
	for path := range p.skipped {
		gone[path] = !paths[path]
	}
	for path, stale := range gone {
		if stale {
			removed = append(removed, path)
		}
	}
	slices.Sort(removed)
	return changed, removed, nil
}

// update analyzes the changed paths in chunks. When the pass stops, the
// paths it did not finish lose their definitions, so none are stale.
func (p *symbolPass) update(ctx context.Context, paths []string) error {
	chunk, start, size := []search.Document{}, 0, 0

	for next, path := range paths {
		document, err := p.document(ctx, path)
		if err != nil {
			return err
		}

		length := len(document.Content)
		over := length > 0 && (p.files >= SymbolPassFiles || p.bytes+length > SymbolPassBytes)
		if over || len(chunk) >= chunkFiles || size+length > chunkBytes {
			if err := p.flush(ctx, chunk); err != nil {
				return err
			}
			if p.stop {
				return p.remove(ctx, paths[start:])
			}
			chunk, start, size = []search.Document{}, next, 0
		}
		if over {
			p.stop = true
			return p.remove(ctx, paths[next:])
		}

		if length > 0 {
			p.files++
			p.bytes += length
		}
		chunk = append(chunk, document)
		size += length
	}

	if err := p.flush(ctx, chunk); err != nil {
		return err
	}
	if p.stop {
		return p.remove(ctx, paths[start:])
	}
	return nil
}

// document returns the code document of a path, without content when it is
// in a language without symbol support, which needs none to be left out.
// Binary content, which the index does not hold, becomes a NUL byte, so
// that it is left out as checkout content is.
func (p *symbolPass) document(ctx context.Context, path string) (search.Document, error) {
	document := search.Document{Ref: search.Ref{Kind: search.KindCode, ID: path}}
	if _, known := symbols.DetectLanguage(path); !known {
		return document, nil
	}

	err := p.engine.Read(ctx, p.index, "", search.Filter{Kind: search.KindCode, ID: path}, func(found search.Document) bool {
		document.Version = found.Version
		document.Content = append([]byte{}, found.Content...)
		return false
	})
	if err != nil {
		return search.Document{}, err
	}
	if len(document.Content) == 0 && document.Version != emptyVersion {
		document.Content = []byte{0}
	}
	return document, nil
}

// flush updates the definitions of a chunk and its skip records. A full
// index stops the pass.
func (p *symbolPass) flush(ctx context.Context, chunk []search.Document) error {
	if len(chunk) == 0 {
		return nil
	}

	result, err := symbol.Update(ctx, p.analyzer, p.engine, p.index, symbol.Change{Documents: chunk}, symbol.Limits{})
	p.add(result)
	if err != nil {
		if p.halt(err) {
			return nil
		}
		return err
	}

	reasons := make(map[string]symbol.SkipReason, len(result.SkippedPaths))
	for _, skipped := range result.SkippedPaths {
		reasons[skipped.Path] = skipped.Reason
	}
	for _, document := range chunk {
		if _, known := symbols.DetectLanguage(document.ID); !known {
			continue // Always unsupported; status counts these from the code.
		}
		if err := p.mark(ctx, document.ID, reasons[document.ID]); err != nil {
			return err
		}
	}
	return nil
}

// remove drops the definitions and skip records of paths, in chunks.
func (p *symbolPass) remove(ctx context.Context, paths []string) error {
	for start := 0; start < len(paths); start += chunkFiles {
		batch := paths[start:min(start+chunkFiles, len(paths))]

		result, err := symbol.Update(ctx, p.analyzer, p.engine, p.index, symbol.Change{Removed: batch}, symbol.Limits{})
		p.result.Removed += result.Removed
		if err != nil {
			return err
		}

		for _, path := range batch {
			if err := p.mark(ctx, path, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

// mark records why a path was left out, or removes its record when reason
// is empty. A full index stops the pass.
func (p *symbolPass) mark(ctx context.Context, path string, reason symbol.SkipReason) error {
	held, ok := p.skipped[path]
	if held == reason && (ok || reason == "") {
		return nil
	}

	ref := search.Ref{Kind: KindSymbolSkipped, ID: path, Version: string(reason)}
	var err error
	if reason == "" {
		err = p.engine.Remove(ctx, p.index, ref)
		if errors.Is(err, search.ErrNotFound) {
			err = nil
		}
	} else {
		err = p.engine.Put(ctx, p.index, search.Document{Ref: ref, Content: []byte{}})
	}

	switch {
	case err == nil && reason == "":
		delete(p.skipped, path)
	case err == nil:
		p.skipped[path] = reason
	case !p.halt(err):
		return err
	}
	return nil
}

// halt reports whether err is a full index or an oversized record, which
// stop the pass but not the call, and stops it if so.
func (p *symbolPass) halt(err error) bool {
	if !errors.Is(err, search.ErrFull) && !errors.Is(err, search.ErrTooLarge) {
		return false
	}
	p.stop = true
	return true
}

func (p *symbolPass) add(result symbol.UpdateResult) {
	p.result.Stored += result.Stored
	p.result.Symbols += result.Symbols
	p.result.Removed += result.Removed
	p.result.Skipped.Unsupported += result.Skipped.Unsupported
	p.result.Skipped.TooLarge += result.Skipped.TooLarge
	p.result.Skipped.Failed += result.Skipped.Failed
}

func (p *symbolPass) proto() *pb.SymbolPass {
	return &pb.SymbolPass{
		Stored:      count(p.result.Stored),
		Definitions: count(p.result.Symbols),
		Removed:     count(p.result.Removed),
		Skipped:     symbolFileState(p.result.Skipped),
		Full:        p.full,
		Truncated:   p.stop,
	}
}
