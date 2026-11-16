package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-search/library/search"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// failingAnalyzer fails the analysis of some paths, and analyzes the others
// with the shared analyzer.
type failingAnalyzer struct {
	mu   sync.Mutex
	errs map[string]error
}

func (a *failingAnalyzer) Analyze(ctx context.Context, file symbols.File) (symbols.Analysis, error) {
	a.mu.Lock()
	err := a.errs[file.Path]
	a.mu.Unlock()

	if err != nil {
		return symbols.Analysis{}, err
	}
	return analyzer().Analyze(ctx, file)
}

func (a *failingAnalyzer) set(path string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.errs[path] = err
}

// symbolSample is sample with Python, a binary Go file, a file in a
// language without symbol support, and a name after non-ASCII text.
func symbolSample() map[string]string {
	files := sample()
	files["lib/shapes.py"] = "class Shape:\n    def area(self):\n        return 0\n\n\ndef greet_all():\n    pass\n"
	files["lib/binary.go"] = "package lib\x00\n"
	files["lib/wide.go"] = "package lib\n\n/* «ü» */ func Greeter() {}\n"
	files["README.md"] = "Greet people.\n"
	return files
}

// TestIndexedSymbolsMatchUnindexed checks that SearchIndexSymbols and
// SearchCheckoutSymbols return the same definitions for the same files,
// through each transport, and that both transports agree.
func TestIndexedSymbolsMatchUnindexed(t *testing.T) {
	queries := []*pb.SymbolQuery{
		{Text: "Greet"},
		{Text: "Greet", Kind: "function"},
		{Text: "Greet", Kind: "class"},
		{Text: "a", Language: "python"},
		{Text: "Shape", Kind: "class", Language: "python"},
		{Text: "e", PathPrefix: "lib/"},
		{Text: "e", PathPrefix: "cmd/"},
		{Text: "Greet", MaxResults: 1},
		{Text: "missing"},
	}

	var all [][]proto.Message
	for i := range 2 {
		f := newFixture(t, nil)
		f.add(t, checkoutID, "rev1", symbolSample())
		tr := transports(t, f.s, "Bearer "+token)[i]

		indexed := must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID})
		equal(t, tr.name+" pass", indexed.GetSymbols(), &pb.SymbolPass{
			Stored: 5, Definitions: 7, Skipped: &pb.SymbolFileState{Unsupported: 2}, Full: true,
		})
		equal(t, tr.name+" status", indexed.GetIndex().GetSymbols(), indexSymbols(5, 2))

		var responses []proto.Message
		for _, q := range queries {
			stored := must[*pb.SearchIndexSymbolsResponse](t, tr, &pb.SearchIndexSymbolsRequest{Index: "main", Query: q})
			read := must[*pb.SearchCheckoutSymbolsResponse](t, tr, &pb.SearchCheckoutSymbolsRequest{CheckoutId: checkoutID, Query: q})

			name := tr.name + " " + q.String()
			equal(t, name, &pb.SearchIndexSymbolsResponse{Symbols: read.GetSymbols(), Truncated: read.GetTruncated()},
				&pb.SearchIndexSymbolsResponse{Symbols: stored.GetSymbols(), Truncated: stored.GetTruncated()})
			if !stored.GetComplete() || stored.GetRevision() != "rev1" || read.GetRevision() != "rev1" {
				t.Errorf("%s: %v %v", name, stored, read)
			}
			equal(t, name+" files", read.GetFiles(), &pb.FileState{})
			responses = append(responses, stored, read)
		}
		all = append(all, responses)
	}
	for i := range all[0] {
		equal(t, "gRPC and REST", all[1][i], all[0][i])
	}

	// Response 0 is the indexed search for Greet, 1 the unindexed one.
	greet := all[0][0].(*pb.SearchIndexSymbolsResponse).GetSymbols()
	if len(greet) != 3 || greet[0].GetName() != "Greet" || greet[1].GetName() != "TestGreet" {
		t.Fatalf("Greet: %v", greet)
	}
	equal(t, "Greeter", greet[2], &pb.Symbol{
		Name: "Greeter", Kind: "function", Language: "go", Path: "lib/wide.go", Version: greet[2].GetVersion(),
		NameRange:     &pb.Range{StartLine: 3, StartColumn: 16, EndLine: 3, EndColumn: 23},
		Range:         &pb.Range{StartLine: 3, StartColumn: 11, EndLine: 3, EndColumn: 28},
		Preview:       "func Greeter() {}",
		PreviewColumn: 11,
	})
	if skipped := all[0][1].(*pb.SearchCheckoutSymbolsResponse).GetSkipped(); skipped.GetUnsupported() != 2 {
		t.Errorf("unindexed skipped: %v", skipped)
	}
	if one := all[0][14].(*pb.SearchIndexSymbolsResponse); len(one.GetSymbols()) != 1 || !one.GetTruncated() {
		t.Errorf("max_results 1: %v", one)
	}
}

// TestStaleSymbols checks that removed and changed files, and a deleted
// index, leave no definitions that match.
func TestStaleSymbols(t *testing.T) {
	f := newFixture(t, nil)
	tr := transports(t, f.s, "Bearer "+token)[0]
	files := sample()
	f.add(t, checkoutID, "rev1", files)
	index := &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID}
	must[*pb.IndexCheckoutResponse](t, tr, index)

	greet := &pb.SearchIndexSymbolsRequest{Index: "main", Query: &pb.SymbolQuery{Text: "Greet"}}
	if found := must[*pb.SearchIndexSymbolsResponse](t, tr, greet); len(found.GetSymbols()) != 2 {
		t.Fatalf("before: %v", found)
	}

	delete(files, "lib/greet_test.go")
	files["lib/greet.go"] = "package lib\n\nfunc Hello() {}\n"
	f.add(t, checkoutID, "rev2", files)
	changed := must[*pb.IndexCheckoutResponse](t, tr, index)
	equal(t, "pass", changed.GetSymbols(), passOf(1, 1, 1, 0, false))
	if found := must[*pb.SearchIndexSymbolsResponse](t, tr, greet); len(found.GetSymbols()) != 0 || !found.GetComplete() {
		t.Errorf("after removal: %v", found)
	}

	must[*pb.DeleteIndexResponse](t, tr, &pb.DeleteIndexRequest{Index: "main"})
	if got := tr.call(t, greet, &pb.SearchIndexSymbolsResponse{}); got != codes.NotFound {
		t.Errorf("deleted index: %v", got)
	}

	f.add(t, otherID, "rev3", map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: otherID})
	all := &pb.SearchIndexSymbolsRequest{Index: "main", Query: &pb.SymbolQuery{Text: "e"}}
	if found := must[*pb.SearchIndexSymbolsResponse](t, tr, all); len(found.GetSymbols()) != 0 {
		t.Errorf("new index of the same name: %v", found)
	}
}

// TestSymbolSkips checks that files left out are counted in the pass, the
// status and unindexed searches, never fail a call, and that the status
// follows later passes.
func TestSymbolSkips(t *testing.T) {
	failing := &failingAnalyzer{errs: map[string]error{
		"lib/slow.go": symbols.ErrTimeout,
		"lib/big.go":  symbols.ErrMemory,
		"lib/huge.go": symbols.ErrTooLarge,
	}}
	f := newFixture(t, func(c *Config) { c.Analyzer = failing })
	tr := transports(t, f.s, "Bearer "+token)[1]

	files := symbolSample()
	files["lib/slow.go"] = "package lib\n\nfunc Slow() {}\n"
	files["lib/big.go"] = "package lib\n\nfunc Big() {}\n"
	files["lib/huge.go"] = "package lib\n\nfunc Huge() {}\n"
	f.add(t, checkoutID, "rev1", files)
	index := &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID}

	skipped := &pb.SymbolFileState{Unsupported: 2, TooLarge: 1, Failed: 2}
	first := must[*pb.IndexCheckoutResponse](t, tr, index)
	equal(t, "pass", first.GetSymbols().GetSkipped(), skipped)
	equal(t, "status", first.GetIndex().GetSymbols().GetSkipped(), skipped)
	if !first.GetIndex().GetSymbols().GetComplete() {
		t.Errorf("a pass with skipped files is complete: %v", first.GetIndex())
	}
	read := must[*pb.SearchCheckoutSymbolsResponse](t, tr, &pb.SearchCheckoutSymbolsRequest{
		CheckoutId: checkoutID, Query: &pb.SymbolQuery{Text: "e"},
	})
	equal(t, "unindexed", read.GetSkipped(), skipped)

	failing.set("lib/slow.go", nil)
	files["lib/slow.go"] += "\nfunc Quick() {}\n"
	delete(files, "lib/big.go")
	f.add(t, checkoutID, "rev2", files)
	second := must[*pb.IndexCheckoutResponse](t, tr, index)
	equal(t, "second pass", second.GetSymbols(), passOf(1, 2, 0, 0, false))
	equal(t, "second status", second.GetIndex().GetSymbols(), &pb.IndexSymbols{
		Complete: true, Documents: 6, Skipped: &pb.SymbolFileState{Unsupported: 2, TooLarge: 1},
	})
}

// TestFailedSymbolPass checks that an analyzer error fails the indexing
// without naming a file, leaves the symbols incomplete, and that the next
// indexing analyzes every file.
func TestFailedSymbolPass(t *testing.T) {
	index := &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID}

	for i := range 2 {
		failing := &failingAnalyzer{errs: map[string]error{}}
		f := newFixture(t, func(c *Config) { c.Analyzer = failing })
		f.add(t, checkoutID, "rev1", sample())
		tr := transports(t, f.s, "Bearer "+token)[i]

		failing.set("lib/greet.go", errors.New("analyze /host/data/lib/greet.go: "+token))
		if got := tr.call(t, index, &pb.IndexCheckoutResponse{}); got != codes.Internal {
			t.Fatalf("%s: %v", tr.name, got)
		}
		status := must[*pb.GetIndexResponse](t, tr, &pb.GetIndexRequest{Index: "main"}).GetIndex()
		if status.GetRevision() != "rev1" || status.GetSymbols().GetComplete() {
			t.Errorf("%s status after failure: %v", tr.name, status)
		}

		failing.set("lib/greet.go", nil)
		again := must[*pb.IndexCheckoutResponse](t, tr, index)
		if !again.GetSymbols().GetFull() || !again.GetIndex().GetSymbols().GetComplete() || again.GetUnchanged() != 3 {
			t.Errorf("%s after recovery: %v", tr.name, again)
		}
	}
}

// TestTruncatedSymbolPass fills an index with code so that its definitions
// do not fit: the indexing succeeds, the symbols stay incomplete with none
// stale, and IndexRepository indexes again rather than report the index up
// to date.
func TestTruncatedSymbolPass(t *testing.T) {
	// Three files, the record, the target and one definition document.
	f := newFixture(t, func(c *Config) {
		c.Engine = search.NewEngine(&search.MemoryStore{}, search.IndexLimits{Documents: 6})
	})
	tr := transports(t, f.s, "Bearer "+token)[0]
	f.checkouts.branch(repository, "rev1", sample())
	index := &pb.IndexRepositoryRequest{Target: repository.proto()}

	for range 2 {
		indexed := must[*pb.IndexRepositoryResponse](t, tr, index)
		pass, status := indexed.GetSymbols(), indexed.GetIndex().GetSymbols()
		if indexed.GetUpToDate() || !pass.GetTruncated() || !pass.GetFull() || status.GetComplete() || status.GetDocuments() != 0 {
			t.Errorf("indexed: %v", indexed)
		}
	}

	found := must[*pb.SearchIndexSymbolsResponse](t, tr, &pb.SearchIndexSymbolsRequest{
		Index: repository.index(), Query: &pb.SymbolQuery{Text: "e"},
	})
	if len(found.GetSymbols()) != 0 || found.GetComplete() || found.GetRevision() != "rev1" {
		t.Errorf("search: %v", found)
	}
	f.clean(t)
}

// TestSymbolSearchErrors checks bad queries, missing indexes and
// checkouts, and the unindexed bounds, on both transports.
func TestSymbolSearchErrors(t *testing.T) {
	f := newFixture(t, nil)
	f.add(t, checkoutID, "rev1", sample())
	many := map[string]string{}
	for i := range 1001 {
		many[fmt.Sprintf("gen/f%04d.go", i)] = "package gen\n"
	}
	many["lib/greet.go"] = sample()["lib/greet.go"]
	f.add(t, otherID, "rev1", many)

	for _, tr := range transports(t, f.s, "Bearer "+token) {
		must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID})
		calls := f.checkouts.calls

		for _, limit := range []int32{-1, MaxResults + 1} {
			q := &pb.SymbolQuery{Text: "Greet", MaxResults: limit}
			for _, req := range []proto.Message{
				&pb.SearchIndexSymbolsRequest{Index: "main", Query: q},
				&pb.SearchCheckoutSymbolsRequest{CheckoutId: checkoutID, Query: q},
			} {
				if got := tr.call(t, req, response(req)); got != codes.InvalidArgument {
					t.Errorf("%s %v: %v", tr.name, req, got)
				}
			}
		}
		if f.checkouts.calls != calls {
			t.Errorf("%s: a bad query resolved a checkout", tr.name)
		}

		q := &pb.SymbolQuery{Text: "Greet"}
		for req, want := range map[proto.Message]codes.Code{
			&pb.SearchIndexSymbolsRequest{Index: "none", Query: q}:                       codes.NotFound,
			&pb.SearchCheckoutSymbolsRequest{CheckoutId: "1" + checkoutID[1:], Query: q}: codes.NotFound,
			&pb.SearchCheckoutSymbolsRequest{CheckoutId: otherID, Query: q}:              codes.ResourceExhausted,
		} {
			if got := tr.call(t, req, response(req)); got != want {
				t.Errorf("%s %v: %v, want %v", tr.name, req, got, want)
			}
		}

		narrow := &pb.SymbolQuery{Text: "Greet", PathPrefix: "lib/"}
		read := must[*pb.SearchCheckoutSymbolsResponse](t, tr, &pb.SearchCheckoutSymbolsRequest{CheckoutId: otherID, Query: narrow})
		if len(read.GetSymbols()) != 1 {
			t.Errorf("%s narrowed: %v", tr.name, read)
		}
	}
}

// countingAnalyzer records the most analyses running at once.
type countingAnalyzer struct {
	mu            sync.Mutex
	running, most int
}

func (a *countingAnalyzer) Analyze(context.Context, symbols.File) (symbols.Analysis, error) {
	a.mu.Lock()
	a.running++
	a.most = max(a.most, a.running)
	a.mu.Unlock()

	time.Sleep(5 * time.Millisecond)

	a.mu.Lock()
	a.running--
	a.mu.Unlock()
	return symbols.Analysis{}, nil
}

func TestBoundedAnalyzer(t *testing.T) {
	counting := &countingAnalyzer{}
	bounded := newBoundedAnalyzer(counting, 2)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := bounded.Analyze(context.Background(), symbols.File{}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if counting.most != 2 {
		t.Errorf("at most %d analyses ran at once, want 2", counting.most)
	}

	bounded.slots <- struct{}{}
	bounded.slots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bounded.Analyze(ctx, symbols.File{}); !errors.Is(err, context.Canceled) {
		t.Errorf("waiting for a slot: %v", err)
	}
}
