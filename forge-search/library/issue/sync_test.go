package issue

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-search/library/search"
)

func searchIndex(t *testing.T, engine *search.Engine, text string, options Options) Results {
	t.Helper()

	results, err := SearchIndex(context.Background(), engine, testIndex, search.Query{Text: text}, options)
	if err != nil {
		t.Fatalf("search index %q: %v", text, err)
	}
	return results
}

func TestSyncAddsIssues(t *testing.T) {
	engine, store := newEngine(t)
	reader := &fakeReader{issues: []issues.Issue{
		newIssue(1, "bug one", "", issues.Open),
		newIssue(2, "other", "a bug", issues.Closed),
	}}

	result := mustSync(t, reader, engine)

	if result.Added != 2 || result.Replaced != 0 || result.Removed != 0 || result.Unchanged != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.SyncedAt.IsZero() || store.puts.Load() != 2 {
		t.Fatalf("synced at %v, %d puts", result.SyncedAt, store.puts.Load())
	}

	got := numbers(searchIndex(t, engine, "bug", Options{}))
	if !slices.Equal(got, []string{"1/title", "2/body"}) {
		t.Fatalf("matches = %v", got)
	}

	synced, err := LastSynced(context.Background(), engine, testIndex, testRepository())
	if err != nil || !synced.Equal(result.SyncedAt) {
		t.Fatalf("last synced = %v, %v; want %v", synced, err, result.SyncedAt)
	}
}

func TestSyncWritesOnlyChanges(t *testing.T) {
	engine, store := newEngine(t)
	reader := &fakeReader{issues: []issues.Issue{
		newIssue(1, "bug", "", issues.Open),
		newIssue(2, "bug", "", issues.Open),
		newIssue(3, "bug", "", issues.Open),
		newIssue(4, "bug", "", issues.Open),
	}}
	mustSync(t, reader, engine)
	store.puts.Store(0)

	reader.set(
		newIssue(1, "bug", "", issues.Open),
		newIssue(2, "bug", "edited", issues.Open),
		newIssue(3, "bug", "", issues.Closed),
		newIssue(5, "bug", "", issues.Open),
	)
	result := mustSync(t, reader, engine)

	if result.Added != 1 || result.Replaced != 2 || result.Removed != 1 || result.Unchanged != 1 {
		t.Fatalf("result = %+v", result)
	}
	if store.puts.Load() != 3 {
		t.Fatalf("%d issue puts, want 3", store.puts.Load())
	}

	store.puts.Store(0)
	result = mustSync(t, reader, engine)
	if result.Unchanged != 4 || result.Added+result.Replaced+result.Removed != 0 || store.puts.Load() != 0 {
		t.Fatalf("unchanged sync = %+v, %d puts", result, store.puts.Load())
	}

	closed := numbers(searchIndex(t, engine, "bug", Options{Filter: Filter{State: issues.Closed}}))
	if !slices.Equal(closed, []string{"3/title"}) {
		t.Fatalf("closed = %v", closed)
	}
}

func TestSyncReplacesOnTitleOrURLChange(t *testing.T) {
	engine, _ := newEngine(t)
	reader := &fakeReader{issues: []issues.Issue{newIssue(1, "bug", "", issues.Open)}}
	mustSync(t, reader, engine)

	moved := newIssue(1, "bug", "", issues.Open)
	moved.WebURL = "https://example.com/1"
	reader.set(moved)

	if result := mustSync(t, reader, engine); result.Replaced != 1 {
		t.Fatalf("result = %+v", result)
	}
	results := searchIndex(t, engine, "bug", Options{})
	if len(results.Matches) != 1 || results.Matches[0].Issue.WebURL != moved.WebURL {
		t.Fatalf("matches = %+v", results.Matches)
	}
}

func TestSyncLeavesOtherDocuments(t *testing.T) {
	engine, _ := newEngine(t)
	ctx := context.Background()

	other := issues.RepositoryRef{Source: "github", Repository: "owner/name-2"}
	otherReader := &fakeReader{issues: []issues.Issue{newIssue(7, "bug", "", issues.Open)}}
	if _, err := Sync(ctx, otherReader, engine, testIndex, other, Limits{}); err != nil {
		t.Fatal(err)
	}
	file := search.Document{Ref: search.Ref{Kind: search.KindCode, Source: "checkout", ID: "bug.txt", Version: "1"}, Content: []byte("bug")}
	if err := engine.Put(ctx, testIndex, file); err != nil {
		t.Fatal(err)
	}

	mustSync(t, &fakeReader{issues: []issues.Issue{newIssue(1, "bug", "", issues.Open)}}, engine)
	mustSync(t, &fakeReader{}, engine)

	refs, err := engine.Documents(ctx, testIndex, search.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, ref := range refs {
		ids = append(ids, string(ref.Kind)+":"+ref.ID)
	}
	want := []string{
		"code:bug.txt",
		"issue-sync:owner/name",
		"issue-sync:owner/name-2",
		"issue:owner/name-2#" + strings.Repeat("0", numberDigits-1) + "7",
	}
	slices.Sort(ids)
	if !slices.Equal(ids, want) {
		t.Fatalf("documents = %v", ids)
	}

	got := numbers(searchIndex(t, engine, "bug", Options{}))
	if !slices.Equal(got, []string{"7/title"}) {
		t.Fatalf("matches = %v", got)
	}
}

func TestSyncLeavesOutLargeIssues(t *testing.T) {
	engine, _ := newEngine(t)
	reader := &fakeReader{issues: []issues.Issue{
		newIssue(1, "bug", "", issues.Open),
		newIssue(2, "bug", "", issues.Open),
	}}
	mustSync(t, reader, engine)

	reader.set(newIssue(1, "bug", "", issues.Open), newIssue(2, "bug", strings.Repeat("x", 11), issues.Open))
	result, err := Sync(context.Background(), reader, engine, testIndex, testRepository(), Limits{BodyBytes: 10})
	if err != nil {
		t.Fatal(err)
	}

	if result.Issues.TooLarge != 1 || result.Removed != 1 || result.Unchanged != 1 {
		t.Fatalf("result = %+v", result)
	}
	if got := numbers(searchIndex(t, engine, "bug", Options{})); !slices.Equal(got, []string{"1/title"}) {
		t.Fatalf("matches = %v", got)
	}
}

func TestSyncTooManyIssuesChangesNothing(t *testing.T) {
	engine, store := newEngine(t)
	reader := &fakeReader{issues: []issues.Issue{newIssue(1, "bug", "", issues.Open)}}
	mustSync(t, reader, engine)
	store.puts.Store(0)

	reader.set(newIssue(1, "changed", "", issues.Open), newIssue(2, "bug", "", issues.Open))
	_, err := Sync(context.Background(), reader, engine, testIndex, testRepository(), Limits{Issues: 1})
	if !errors.Is(err, search.ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if store.puts.Load() != 0 {
		t.Fatalf("%d issue puts", store.puts.Load())
	}
	assertUnsynced(t, engine)
}

func TestSyncReaderFailureLeavesIndexUnsynced(t *testing.T) {
	engine, _ := newEngine(t)
	reader := &fakeReader{issues: []issues.Issue{newIssue(1, "bug", "", issues.Open)}}
	mustSync(t, reader, engine)

	failure := errors.New("forge down " + "secret-text")
	reader.err = failure
	_, err := Sync(context.Background(), reader, engine, testIndex, testRepository(), Limits{})
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v", err)
	}
	assertUnsynced(t, engine)

	reader.err = nil
	reader.set(newIssue(2, "bug", "", issues.Open))
	mustSync(t, reader, engine)
	assertEquivalent(t, reader, engine, "bug", Options{})
}

func TestSyncCanceledDuringListing(t *testing.T) {
	engine, _ := newEngine(t)
	reader := &fakeReader{issues: []issues.Issue{newIssue(1, "bug", "", issues.Open)}}
	mustSync(t, reader, engine)

	ctx, cancel := context.WithCancel(context.Background())
	reader.listing = cancel
	_, err := Sync(ctx, reader, engine, testIndex, testRepository(), Limits{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	assertUnsynced(t, engine)

	reader.listing = nil
	reader.set(newIssue(1, "bug", "edited", issues.Open))
	mustSync(t, reader, engine)
	assertEquivalent(t, reader, engine, "edited", Options{})
}

func TestSyncStoreFailureIsCorrectedByTheNext(t *testing.T) {
	engine, store := newEngine(t)
	reader := &fakeReader{issues: []issues.Issue{
		newIssue(1, "bug", "", issues.Open),
		newIssue(2, "bug", "", issues.Open),
		newIssue(3, "bug", "", issues.Open),
	}}
	mustSync(t, reader, engine)

	reader.set(
		newIssue(1, "bug one", "", issues.Closed),
		newIssue(3, "bug three", "", issues.Closed),
		newIssue(4, "bug four", "", issues.Open),
	)
	failing := documentID("owner/name", 3)
	store.fail = func(entry search.Entry) bool { return entry.Document.ID == failing }

	result, err := Sync(context.Background(), reader, engine, testIndex, testRepository(), Limits{})
	if !errors.Is(err, errInjected) || !result.SyncedAt.IsZero() {
		t.Fatalf("sync = %+v, %v", result, err)
	}
	if strings.Contains(err.Error(), "bug three") || !strings.Contains(err.Error(), "issue 3") {
		t.Fatalf("err = %v", err)
	}
	if result.Removed != 1 || result.Replaced != 1 {
		t.Fatalf("partial result = %+v", result)
	}
	assertUnsynced(t, engine)

	store.fail = nil
	result = mustSync(t, reader, engine)
	if result.Replaced != 1 || result.Added != 1 || result.Unchanged != 1 {
		t.Fatalf("result = %+v", result)
	}
	assertEquivalent(t, reader, engine, "bug", Options{})
}

func TestSyncInvalidRequests(t *testing.T) {
	engine, _ := newEngine(t)
	ctx := context.Background()
	reader := &fakeReader{}

	repositories := []issues.RepositoryRef{
		{Repository: "owner/name"},
		{Source: "github"},
		{Source: "github", Repository: "owner/name#2"},
	}
	for _, repository := range repositories {
		if _, err := Sync(ctx, reader, engine, testIndex, repository, Limits{}); !errors.Is(err, search.ErrInvalid) {
			t.Errorf("%+v: err = %v", repository, err)
		}
		if _, err := LastSynced(ctx, engine, testIndex, repository); !errors.Is(err, search.ErrInvalid) {
			t.Errorf("%+v: last synced err = %v", repository, err)
		}
	}
	if len(reader.asked) != 0 {
		t.Fatalf("reader asked %d times", len(reader.asked))
	}

	if _, err := Sync(ctx, reader, engine, "missing", testRepository(), Limits{}); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("missing index: err = %v", err)
	}

	reader.set(newIssue(1, "a", "", issues.Open), newIssue(1, "b", "", issues.Open))
	if _, err := Sync(ctx, reader, engine, testIndex, testRepository(), Limits{}); !errors.Is(err, search.ErrInvalid) {
		t.Fatalf("duplicate: err = %v", err)
	}
}

func TestLastSyncedWithoutSync(t *testing.T) {
	engine, _ := newEngine(t)

	synced, err := LastSynced(context.Background(), engine, testIndex, testRepository())
	if err != nil || !synced.IsZero() {
		t.Fatalf("last synced = %v, %v", synced, err)
	}
}

func assertUnsynced(t *testing.T, engine *search.Engine) {
	t.Helper()

	synced, err := LastSynced(context.Background(), engine, testIndex, testRepository())
	if err != nil || !synced.IsZero() {
		t.Fatalf("last synced = %v, %v; want none", synced, err)
	}
}
