package storetest

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// Size of the equivalence step.
const (
	equivalenceRounds  = 30
	updatesPerRound    = 12
	searchesPerRound   = 20
	equivalenceResults = 50
)

// generator makes the documents, queries and filters of the equivalence
// step. The same seed gives the same sequence.
type generator struct {
	random *rand.Rand
}

func newGenerator(seed uint64) generator {
	//nolint:gosec // test data must be reproducible, not secret.
	return generator{random: rand.New(rand.NewPCG(seed, seed))}
}

func (g generator) pick(values ...string) string {
	return values[g.random.IntN(len(values))]
}

// text joins up to most fragments that overlap in many ways, so that short
// and long queries match often and at many columns.
func (g generator) text(most int) string {
	var text strings.Builder
	for range g.random.IntN(most + 1) {
		text.WriteString(g.pick("a", "ab", "abc", "bca", "cab", "é", "日本", "😀", " ", "\t", "func", "main", "()"))
	}
	return text.String()
}

// content returns a document body: a few lines, sometimes one long enough to
// cut its preview, sometimes binary.
func (g generator) content() []byte {
	var content strings.Builder

	for range g.random.IntN(8) {
		content.WriteString(g.text(6))
		if g.random.IntN(10) == 0 {
			content.WriteString(strings.Repeat(g.text(2), 150))
		}
		content.WriteString(g.pick("\n", "\n", "\r\n", ""))
	}
	if g.random.IntN(8) == 0 {
		content.WriteString(g.pick("\x00", "\xff"))
	}
	return []byte(content.String())
}

func (g generator) ref() search.Ref {
	return search.Ref{
		Kind:   search.Kind(g.pick("code", "issue")),
		Source: g.pick("", "one", "two"),
		ID:     g.pick("a", "b", "dir/a", "dir/b", "dir/sub/c", "é"),
	}
}

func (g generator) filter() search.Filter {
	if g.random.IntN(2) == 0 {
		return search.Filter{}
	}
	return search.Filter{
		Kind:     search.Kind(g.pick("", "", "code", "issue")),
		Source:   g.pick("", "", "one", "two"),
		ID:       g.pick("", "", "", "a", "dir/a"),
		IDPrefix: g.pick("", "", "dir/", "dir/s", "a"),
	}
}

// query returns one to five runes of generated text, so that some queries
// are too short for the index to narrow.
func (g generator) query() search.Query {
	runes := []rune(g.text(3) + g.pick("a", "b", "é", "(", "m"))

	start := g.random.IntN(len(runes))
	end := min(len(runes), start+1+g.random.IntN(5))

	limits := []int{0, 1, 2, 5, equivalenceResults}
	return search.Query{Text: string(runes[start:end]), Limit: limits[g.random.IntN(len(limits))]}
}

// model is what the index should hold, by identity.
type model map[search.Ref]search.Document

func (m model) selected(filter search.Filter) []search.Document {
	var documents []search.Document
	for _, document := range m {
		if filter.Matches(document.Ref) {
			documents = append(documents, document)
		}
	}
	return documents
}

// equivalence checks that indexed search returns exactly what unindexed
// search returns for the same documents, while documents are added, replaced
// and removed.
func (r *run) equivalence() {
	engine := search.NewEngine(r.store, search.IndexLimits{Results: equivalenceResults})
	r.wantErr(engine.CreateIndex(r.ctx, mainIndex), nil)

	g := newGenerator(85)
	held := model{}

	for round := range equivalenceRounds {
		for range updatesPerRound {
			r.change(engine, g, held, round)
		}
		for range searchesPerRound {
			r.compare(engine, held, g.query(), g.filter())
		}
		r.compareListing(engine, held, g.filter())
	}

	for key := range held {
		r.wantErr(engine.Remove(r.ctx, mainIndex, key), nil)
		delete(held, key)
	}
	for range searchesPerRound {
		r.compare(engine, held, g.query(), search.Filter{})
	}
}

// change adds, replaces or removes one document in the index and the model.
func (r *run) change(engine *search.Engine, g generator, held model, round int) {
	r.t.Helper()
	key := g.ref()

	if _, exists := held[key]; exists && g.random.IntN(3) == 0 {
		r.wantErr(engine.Remove(r.ctx, mainIndex, key), nil)
		delete(held, key)
		return
	}

	document := search.Document{Ref: key, Content: g.content()}
	document.Version = fmt.Sprintf("v%d", round)

	r.wantErr(engine.Put(r.ctx, mainIndex, document), nil)
	held[key] = document
}

// compare runs one query in both modes.
func (r *run) compare(engine *search.Engine, held model, query search.Query, filter search.Filter) {
	r.t.Helper()

	want, err := search.Unindexed(r.ctx, query, held.selected(filter), search.Limits{Results: equivalenceResults})
	r.wantErr(err, nil)
	got, err := engine.Search(r.ctx, mainIndex, query, filter)
	r.wantErr(err, nil)

	if !reflect.DeepEqual(got, want) {
		r.t.Fatalf("query %q (%d bytes), filter %+v:\nindexed   %+v\nunindexed %+v",
			query.Text, len(query.Text), filter, got, want)
	}
	if !utf8.ValidString(query.Text) {
		r.t.Fatalf("generated query %q is not valid UTF-8", query.Text)
	}
}

// compareListing checks that the index lists the documents of the model.
func (r *run) compareListing(engine *search.Engine, held model, filter search.Filter) {
	r.t.Helper()

	got, err := engine.Documents(r.ctx, mainIndex, filter)
	r.wantErr(err, nil)

	want := []search.Ref{}
	for _, document := range held.selected(filter) {
		want = append(want, document.Ref)
	}
	slices.SortFunc(want, func(a, b search.Ref) int {
		return strings.Compare(string(a.Kind)+"\x00"+a.Source+"\x00"+a.ID, string(b.Kind)+"\x00"+b.Source+"\x00"+b.ID)
	})

	if !slices.Equal(got, want) {
		r.t.Fatalf("filter %+v: listed %+v, want %+v", filter, got, want)
	}
}
