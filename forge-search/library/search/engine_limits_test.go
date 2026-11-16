package search

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// untouched is a store that fails the test when the engine reaches it.
type untouched struct {
	Store

	t *testing.T
}

func (u untouched) reached() { u.t.Helper(); u.t.Error("the store was reached") }

func (u untouched) CreateIndex(context.Context, string) error { u.reached(); return nil }
func (u untouched) DeleteIndex(context.Context, string) error { u.reached(); return nil }

func (u untouched) Index(context.Context, string) (IndexInfo, error) {
	u.reached()
	return IndexInfo{}, nil
}

func (u untouched) SetMarker(context.Context, string, string) error { u.reached(); return nil }

func (u untouched) Put(context.Context, string, Entry, Capacity) error { u.reached(); return nil }
func (u untouched) Remove(context.Context, string, Ref) error          { u.reached(); return nil }

func (u untouched) Refs(context.Context, string, Selection, func(Ref) bool) error {
	u.reached()
	return nil
}

func (u untouched) Documents(context.Context, string, Selection, func(Document) bool) error {
	u.reached()
	return nil
}

// everyCall runs each engine method that takes an index name, a document
// identity, a marker or a filter, and returns their errors by method name.
func everyCall(engine *Engine, index string, document Document, marker string, filter Filter) map[string]error {
	ctx := context.Background()
	errs := map[string]error{}

	errs["CreateIndex"] = engine.CreateIndex(ctx, index)
	errs["DeleteIndex"] = engine.DeleteIndex(ctx, index)
	_, errs["Index"] = engine.Index(ctx, index)
	errs["SetMarker"] = engine.SetMarker(ctx, index, marker)
	errs["Put"] = engine.Put(ctx, index, document)
	errs["Remove"] = engine.Remove(ctx, index, document.Ref)
	_, errs["Documents"] = engine.Documents(ctx, index, filter)
	_, errs["Search"] = engine.Search(ctx, index, Query{Text: "needle"}, filter)
	errs["Read"] = engine.Read(ctx, index, "needle", filter, func(Document) bool { return true })
	return errs
}

func TestEngineRejectsInvalidIndexNames(t *testing.T) {
	engine := NewEngine(untouched{t: t}, IndexLimits{})

	names := []string{
		"",
		"..",
		"a..b",
		"../a",
		"a/b",
		"/a",
		`a\b`,
		".a",
		"-a",
		"_a",
		"A",
		"a b",
		"a\x00",
		"a\n",
		"café",
		"a:b",
		"a*",
		secret + "/",
		strings.Repeat("a", MaxIndexNameBytes+1),
	}
	for _, name := range names {
		for method, err := range everyCall(engine, name, code("a", secret), "", Filter{}) {
			if err == nil {
				t.Errorf("%s accepted index name %q", method, name)
				continue
			}
			wantError(t, err, ErrInvalid)
			if strings.Contains(err.Error(), name) && name != "" {
				t.Errorf("%s: error %q repeats the name", method, err)
			}
		}
	}
}

func TestEngineAcceptsValidIndexNames(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(&MemoryStore{}, IndexLimits{})

	names := []string{"a", "0", "main", "r59q.forge-tools_main", "a.b.c", "1-2", strings.Repeat("a", MaxIndexNameBytes)}
	for _, name := range names {
		if err := engine.CreateIndex(ctx, name); err != nil {
			t.Errorf("CreateIndex(%q): %v", name, err)
		}
	}
	if infos, err := engine.Indexes(ctx); err != nil || len(infos) != len(names) {
		t.Errorf("Indexes = %d indexes, %v, want %d", len(infos), err, len(names))
	}
}

func TestEngineRejectsInvalidArguments(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(untouched{t: t}, IndexLimits{})

	refs := []Ref{
		{Source: "checkout", ID: "a"},
		{Kind: KindCode, Source: "checkout"},
		{Kind: "bad\x00kind", ID: "a"},
		{Kind: KindCode, Source: "bad\xffsource", ID: "a"},
		{Kind: KindCode, ID: "a", Version: "bad\x00version"},
		{Kind: KindCode, ID: strings.Repeat("a", MaxNameBytes+1)},
	}
	for _, ref := range refs {
		wantError(t, engine.Put(ctx, testIndex, Document{Ref: ref, Content: []byte(secret)}), ErrInvalid)
		wantError(t, engine.Remove(ctx, testIndex, ref), ErrInvalid)
	}

	for _, marker := range []string{"bad\x00marker", "bad\xffmarker", strings.Repeat("a", MaxMarkerBytes+1)} {
		wantError(t, engine.SetMarker(ctx, testIndex, marker), ErrInvalid)
	}

	filters := []Filter{
		{Kind: "bad\x00kind"},
		{Source: "bad\xffsource"},
		{ID: "bad\x00id"},
		{IDPrefix: strings.Repeat("a", MaxNameBytes+1)},
	}
	for _, filter := range filters {
		_, err := engine.Documents(ctx, testIndex, filter)
		wantError(t, err, ErrInvalid)
		_, err = engine.Search(ctx, testIndex, Query{Text: "needle"}, filter)
		wantError(t, err, ErrInvalid)
	}

	queries := []Query{
		{},
		{Text: "a\nb"},
		{Text: "a\x00"},
		{Text: "\xff"},
		{Text: strings.Repeat("a", MaxQueryBytes+1)},
		{Text: "a", Limit: -1},
		{Text: "a", Limit: 1001},
	}
	for _, query := range queries {
		_, err := engine.Search(ctx, testIndex, query, Filter{})
		wantError(t, err, ErrInvalid)
	}
}

func TestEngineLimitsDocumentBytes(t *testing.T) {
	ctx := context.Background()
	engine, _ := newIndex(t, IndexLimits{DocumentBytes: len(secret)}, code("a", secret))

	wantError(t, engine.Put(ctx, testIndex, code("b", secret+"!")), ErrTooLarge)
	wantError(t, engine.Put(ctx, testIndex, code("a", secret+"!")), ErrTooLarge)
	wantError(t, engine.Put(ctx, testIndex, code("b", secret+"\x00")), ErrTooLarge)

	if got := matched(mustFind(t, engine, Query{Text: secret}, Filter{})); !slices.Equal(got, []string{"a"}) {
		t.Errorf("matched %q, want the first document unchanged", got)
	}
}

func TestEngineLimitsIndexDocuments(t *testing.T) {
	ctx := context.Background()
	engine, _ := newIndex(t, IndexLimits{Documents: 2}, code("a", secret), code("b", secret))

	wantError(t, engine.Put(ctx, testIndex, code("c", secret)), ErrFull)
	wantError(t, engine.Put(ctx, testIndex, code("c", "\x00")), ErrFull)
	mustPut(t, engine, code("b", "replaced"))

	wantError(t, engine.Remove(ctx, testIndex, code("a", "").Ref), nil)
	mustPut(t, engine, code("c", secret))

	listed, err := engine.Documents(ctx, testIndex, Filter{})
	wantError(t, err, nil)
	if want := []Ref{code("b", "").Ref, code("c", "").Ref}; !slices.Equal(listed, want) {
		t.Errorf("documents = %+v, want %+v", listed, want)
	}
}

func TestEngineLimitsIndexBytes(t *testing.T) {
	ctx := context.Background()
	engine, _ := newIndex(t, IndexLimits{Bytes: int64(2 * len(secret))}, code("a", secret), code("b", secret))

	wantError(t, engine.Put(ctx, testIndex, code("c", "x")), ErrFull)
	wantError(t, engine.Put(ctx, testIndex, code("a", secret+"!")), ErrFull)

	want := IndexInfo{Name: testIndex, Documents: 2, Bytes: int64(2 * len(secret))}
	if info, err := engine.Index(ctx, testIndex); err != nil || info != want {
		t.Errorf("Index = %+v, %v, want %+v", info, err, want)
	}
	if got := matched(mustFind(t, engine, Query{Text: secret}, Filter{})); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("matched %q, want both documents unchanged", got)
	}

	mustPut(t, engine, code("a", "x"))
	mustPut(t, engine, code("c", secret[1:]))
	mustPut(t, engine, code("d", ""))
}

func TestEngineLimitsResults(t *testing.T) {
	engine, _ := newIndex(t, IndexLimits{Results: 3}, code("a", "x\nx"), code("b", "x\nx"))

	tests := []struct {
		limit     int
		matches   int
		truncated bool
	}{{0, 3, true}, {1, 1, true}, {3, 3, true}}
	for _, test := range tests {
		results := mustFind(t, engine, Query{Text: "x", Limit: test.limit}, Filter{})
		if len(results.Matches) != test.matches || results.State.Truncated != test.truncated {
			t.Errorf("limit %d: %d matches, state %+v", test.limit, len(results.Matches), results.State)
		}
	}

	results := mustFind(t, engine, Query{Text: "x"}, Filter{ID: "a"})
	if len(results.Matches) != 2 || results.State.Truncated {
		t.Errorf("complete search: %d matches, state %+v", len(results.Matches), results.State)
	}

	_, err := engine.Search(context.Background(), testIndex, Query{Text: "x", Limit: 4}, Filter{})
	wantError(t, err, ErrInvalid)
}

func TestDefaultIndexLimits(t *testing.T) {
	want := IndexLimits{DocumentBytes: 1 << 20, Documents: 100000, Bytes: 1 << 30, Results: 1000}

	if got := DefaultIndexLimits(); got != want {
		t.Errorf("DefaultIndexLimits() = %+v, want %+v", got, want)
	}
	if got := NewEngine(&MemoryStore{}, IndexLimits{Documents: -1}).limits; got != want {
		t.Errorf("engine limits = %+v, want the defaults", got)
	}
}

func TestEngineForgetsReplacedAndRemovedContent(t *testing.T) {
	ctx := context.Background()
	engine, store := newIndex(t, IndexLimits{})

	absent := func(step string) {
		t.Helper()

		if holdsContent(store, secret) {
			t.Errorf("after %s the store still holds the content", step)
		}
		for _, text := range []string{secret, secret[:5], secret[:2]} {
			for _, index := range []string{testIndex, "other"} {
				results, err := engine.Search(ctx, index, Query{Text: text}, Filter{})
				if err == nil && len(results.Matches) != 0 {
					t.Errorf("after %s, %q still matches in %s", step, text, index)
				}
			}
		}
	}

	mustPut(t, engine, code("a", "before\n"+secret+"\nafter"))
	if !holdsContent(store, secret) || len(mustFind(t, engine, Query{Text: secret}, Filter{}).Matches) != 1 {
		t.Fatal("the content is not indexed; the test proves nothing")
	}

	mustPut(t, engine, code("a", "public text"))
	absent("replacing")

	mustPut(t, engine, code("a", secret))
	wantError(t, engine.Remove(ctx, testIndex, code("a", "").Ref), nil)
	absent("removing")

	wantError(t, engine.CreateIndex(ctx, "other"), nil)
	wantError(t, engine.Put(ctx, "other", code("a", secret)), nil)
	wantError(t, engine.DeleteIndex(ctx, "other"), nil)
	absent("deleting the index")

	wantError(t, engine.CreateIndex(ctx, "other"), nil)
	absent("creating the index again")
}
