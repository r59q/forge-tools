package storetest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// Size of the benchmark corpus, and the result limit of its searches.
const (
	corpusBytes   = 32 << 20
	documentBytes = 16 << 10
	vocabulary    = 2000
	resultLimit   = 100000
)

// needle is text that only a few documents of the corpus hold.
const needle = "QX7_needle_QX7"

// Corpus returns generated text documents of about the given total and
// document size. Words follow a skewed distribution, as in code: a few are
// everywhere, most are rare. Three documents hold a line with a needle. The
// same sizes always give the same corpus.
func Corpus(totalBytes, documentBytes int) []search.Document {
	random, words := newVocabulary()

	count := max(4, totalBytes/documentBytes)
	documents := make([]search.Document, count)

	for index := range documents {
		var content strings.Builder
		if index%(count/3+1) == count/6 {
			content.WriteString("const " + needle + " = true\n")
		}
		for content.Len() < documentBytes {
			line(&content, random, words)
		}
		documents[index] = search.Document{
			Ref:     search.Ref{Kind: search.KindCode, Source: "corpus", ID: fmt.Sprintf("dir%02d/file%05d.txt", index%50, index)},
			Content: []byte(content.String()),
		}
	}
	return documents
}

// newVocabulary returns the words of the corpus, the most frequent first,
// and the generator that continues after them.
func newVocabulary() (*rand.Rand, []string) {
	//nolint:gosec // benchmark data must be reproducible, not secret.
	random := rand.New(rand.NewPCG(1, 2))

	words := make([]string, vocabulary)
	for index := range words {
		words[index] = word(random)
	}
	return random, words
}

func word(random *rand.Rand) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"

	letters := make([]byte, 3+random.IntN(8))
	for index := range letters {
		letters[index] = alphabet[random.IntN(len(alphabet))]
	}
	return string(letters)
}

func line(content *strings.Builder, random *rand.Rand, words []string) {
	for range 4 + random.IntN(9) {
		skew := random.Float64()
		content.WriteString(words[int(skew*skew*skew*float64(len(words)))])
		content.WriteByte(' ')
	}
	content.WriteByte('\n')
}

// Benchmark measures both search modes on the same generated corpus: every
// query once unindexed, over the documents in memory, and once indexed, on
// an engine that keeps them in the store.
//
// The queries are an identifier that three documents hold, a line of common
// words that one document holds, the rarest word of the corpus, and two
// bytes, which no index can narrow.
func Benchmark(b *testing.B, open Open) {
	ctx := context.Background()
	documents := Corpus(corpusBytes, documentBytes)
	_, words := newVocabulary()

	engine := search.NewEngine(open(b), search.IndexLimits{Results: resultLimit})
	if err := engine.CreateIndex(ctx, mainIndex); err != nil {
		b.Fatal(err)
	}
	for _, document := range documents {
		if err := engine.Put(ctx, mainIndex, document); err != nil {
			b.Fatal(err)
		}
	}

	queries := []struct{ name, text string }{
		{"identifier", needle},
		{"phrase", lastLine(documents[len(documents)/2])},
		{"word", words[len(words)-1]},
		{"short", needle[:2]},
	}
	limits := search.Limits{Documents: len(documents), TotalBytes: 2 * corpusBytes, Results: resultLimit}

	for _, query := range queries {
		b.Run(query.name+"/unindexed", func(b *testing.B) {
			for b.Loop() {
				results, err := search.Unindexed(ctx, search.Query{Text: query.text}, documents, limits)
				check(b, results, err)
			}
		})
		b.Run(query.name+"/indexed", func(b *testing.B) {
			for b.Loop() {
				results, err := engine.Search(ctx, mainIndex, search.Query{Text: query.text}, search.Filter{})
				check(b, results, err)
			}
		})
	}
}

func lastLine(document search.Document) string {
	lines := strings.Split(strings.TrimSpace(string(document.Content)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// check fails a benchmark whose search found nothing or was cut short.
func check(b *testing.B, results search.Results, err error) {
	b.Helper()
	if err != nil {
		b.Fatal(err)
	}
	if len(results.Matches) == 0 || results.State != (search.State{}) {
		b.Fatalf("%d matches, state %+v", len(results.Matches), results.State)
	}
}
