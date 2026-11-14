package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// Sizes of the concurrency steps: readers run beside one writer until the
// writer has done minWrites updates and the readers minReads reads.
const (
	readers   = 4
	minWrites = 100
	minReads  = 400
)

// during runs read in several goroutines while update runs round after
// round, and fails the step on the first error of either.
func (r *run) during(update func(round int) error, read func() error) {
	var (
		group  sync.WaitGroup
		reads  atomic.Int64
		failed atomic.Bool
	)
	stop := make(chan struct{})

	fail := func(err error) {
		failed.Store(true)
		r.t.Error(err)
	}

	group.Go(func() {
		defer close(stop)
		for round := 0; !failed.Load() && (round < minWrites || reads.Load() < minReads); round++ {
			if err := update(round); err != nil {
				fail(err)
			}
		}
	})
	for range readers {
		group.Go(func() {
			for !failed.Load() {
				if err := read(); err != nil {
					fail(err)
				}
				reads.Add(1)

				select {
				case <-stop:
					return
				default:
				}
			}
		})
	}
	group.Wait()
}

// atomicUpdates checks that a reader sees an entry with the content and
// terms of one Put, never a mix of two.
func (r *run) atomicUpdates() {
	ctx, s := r.ctx, r.store
	r.create(mainIndex)

	versions := []search.Entry{
		entry("x", "old", "old content", 1),
		entry("x", "new", "new and longer content", 2),
	}
	r.put(mainIndex, versions[0])

	update := func(round int) error {
		return s.Put(ctx, mainIndex, versions[(round+1)%2], roomy())
	}
	read := func() error {
		for position, version := range versions {
			if err := holdsOnly(ctx, s, terms(search.Term(position+1)), version, 0); err != nil {
				return err
			}
		}
		return errors.Join(
			holdsOnly(ctx, s, search.Selection{}, versions[0], 1),
			sizeOfOne(ctx, s, versions),
		)
	}
	r.during(update, read)
}

// holdsOnly checks that a selection picks at least least documents, each
// either want or, when the selection has no terms, any stored version.
func holdsOnly(ctx context.Context, s search.Store, selection search.Selection, want search.Entry, least int) error {
	count := 0
	mixed := false

	err := s.Documents(ctx, mainIndex, selection, func(document search.Document) bool {
		count++
		content := string(document.Content)

		exact := document.Ref == want.Document.Ref && content == string(want.Document.Content)
		whole := strings.HasPrefix(content, document.Version)
		if len(selection.Terms) > 0 && !exact || !whole {
			mixed = true
		}
		return true
	})
	switch {
	case err != nil:
		return err
	case mixed:
		return fmt.Errorf("selection %+v returned a document of mixed versions", selection)
	case count < least || count > 1:
		return fmt.Errorf("selection %+v returned %d documents", selection, count)
	}
	return nil
}

// sizeOfOne checks that the index reports the size of exactly one version.
func sizeOfOne(ctx context.Context, s search.Store, versions []search.Entry) error {
	info, err := s.Index(ctx, mainIndex)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if info.Documents == 1 && info.Bytes == int64(len(version.Document.Content)) {
			return nil
		}
	}
	return fmt.Errorf("index reports %d documents with %d bytes", info.Documents, info.Bytes)
}

// Shape of the documents the concurrent searches replace: every version has
// its own number of lines, each holding a shared word and the version.
const (
	replaced = 8
	oldLines = 3
	newLines = 5
)

func versioned(id int, version string, lines int) search.Document {
	return search.Document{
		Ref:     search.Ref{Kind: search.KindCode, Source: "checkout", ID: fmt.Sprintf("file-%d", id), Version: version},
		Content: []byte(strings.Repeat("shared "+version+"-token\n", lines)),
	}
}

// concurrentSearches checks that a search during updates sees every document
// entirely old or entirely new.
func (r *run) concurrentSearches() {
	ctx := r.ctx
	engine := search.NewEngine(r.store, search.IndexLimits{})
	r.wantErr(engine.CreateIndex(ctx, mainIndex), nil)

	for id := range replaced {
		r.wantErr(engine.Put(ctx, mainIndex, versioned(id, "old", oldLines)), nil)
	}

	update := func(round int) error {
		if round/replaced%2 == 0 {
			return engine.Put(ctx, mainIndex, versioned(round%replaced, "new", newLines))
		}
		return engine.Put(ctx, mainIndex, versioned(round%replaced, "old", oldLines))
	}
	read := func() error {
		return errors.Join(
			wholeDocuments(ctx, engine, "shared", replaced),
			wholeDocuments(ctx, engine, "old-token", 0),
			wholeDocuments(ctx, engine, "new-token", 0),
			wholeDocuments(ctx, engine, "ne", 0),
		)
	}
	r.during(update, read)
}

// wholeDocuments searches for text and checks that every matching document
// shows all lines of one version, and that at least least documents match.
func wholeDocuments(ctx context.Context, engine *search.Engine, text string, least int) error {
	results, err := engine.Search(ctx, mainIndex, search.Query{Text: text}, search.Filter{})
	if err != nil {
		return err
	}

	lines := map[search.Ref]int{}
	for _, match := range results.Matches {
		if !strings.Contains(match.Preview, match.Document.Version+"-token") {
			return fmt.Errorf("query %q: version %q matched a line of another version", text, match.Document.Version)
		}
		lines[match.Document]++
	}

	if len(lines) < least {
		return fmt.Errorf("query %q: %d documents matched, want %d", text, len(lines), least)
	}
	for ref, count := range lines {
		want := oldLines
		if ref.Version == "new" {
			want = newLines
		}
		if count != want {
			return fmt.Errorf("query %q: %d lines of version %q matched, want %d", text, count, ref.Version, want)
		}
	}
	return nil
}
