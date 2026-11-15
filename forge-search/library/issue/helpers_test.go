package issue

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-search/library/search"
)

const testIndex = "issues-1"

func testRepository() issues.RepositoryRef {
	return issues.RepositoryRef{Source: "github", Repository: "owner/name"}
}

// fakeReader lists a fixed set of issues, or fails.
type fakeReader struct {
	mu     sync.Mutex
	issues []issues.Issue
	err    error
	asked  []issues.ListOptions

	// listing, when set, runs while the reader lists.
	listing func()
}

func (r *fakeReader) List(ctx context.Context, options issues.ListOptions) ([]issues.Issue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.asked = append(r.asked, options)
	if r.listing != nil {
		r.listing()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.err != nil {
		return nil, r.err
	}
	return slices.Clone(r.issues), nil
}

func (r *fakeReader) set(list ...issues.Issue) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.issues = list
}

func newIssue(number int64, title, body string, state issues.State) issues.Issue {
	return issues.Issue{
		Source:     issues.SourceInfo{ID: "github"},
		Repository: "owner/name",
		Number:     number,
		Title:      title,
		Body:       body,
		State:      state,
		WebURL:     "https://github.com/owner/name/issues/" + itoa(number),
	}
}

func itoa(number int64) string {
	return strconv.FormatInt(number, 10)
}

// countingStore counts the entries put, and fails puts once fail returns
// true.
type countingStore struct {
	*search.MemoryStore

	puts atomic.Int64
	fail func(entry search.Entry) bool
}

var errInjected = errors.New("injected store failure")

func (s *countingStore) Put(ctx context.Context, index string, entry search.Entry, capacity search.Capacity) error {
	if s.fail != nil && s.fail(entry) {
		return errInjected
	}
	if entry.Document.Kind == search.KindIssue {
		s.puts.Add(1)
	}
	return s.MemoryStore.Put(ctx, index, entry, capacity)
}

func newEngine(t *testing.T) (*search.Engine, *countingStore) {
	t.Helper()

	store := &countingStore{MemoryStore: &search.MemoryStore{}}
	engine := search.NewEngine(store, search.IndexLimits{})
	if err := engine.CreateIndex(context.Background(), testIndex); err != nil {
		t.Fatal(err)
	}
	return engine, store
}

func mustSync(t *testing.T, reader Reader, engine *search.Engine) SyncResult {
	t.Helper()

	result, err := Sync(context.Background(), reader, engine, testIndex, testRepository(), Limits{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return result
}

func mustSearch(t *testing.T, list []issues.Issue, text string, options Options) Results {
	t.Helper()

	results, err := Search(context.Background(), list, search.Query{Text: text}, options)
	if err != nil {
		t.Fatalf("search %q: %v", text, err)
	}
	return results
}

// numbers returns the issue number and field of each match.
func numbers(results Results) []string {
	named := []string{}
	for _, match := range results.Matches {
		named = append(named, itoa(match.Issue.Number)+"/"+string(match.Field))
	}
	return named
}
