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

func TestSearchFindsTitleAndBody(t *testing.T) {
	list := []issues.Issue{
		newIssue(2, "Crash on start", "Steps:\r\n1. run it\r\n2. it crashes", issues.Open),
		newIssue(10, "Docs", "no match here", issues.Closed),
	}

	results := mustSearch(t, list, "crash", Options{})

	if len(results.Matches) != 1 {
		t.Fatalf("matches = %v", numbers(results))
	}
	match := results.Matches[0]

	want := Match{
		Issue: Info{
			Source:     "github",
			Repository: "owner/name",
			Number:     2,
			Title:      "Crash on start",
			State:      issues.Open,
			WebURL:     "https://github.com/owner/name/issues/2",
		},
		Field:         FieldBody,
		Range:         search.Range{StartLine: 3, StartColumn: 7, EndLine: 3, EndColumn: 12},
		Preview:       "2. it crashes",
		PreviewColumn: 1,
	}
	if match != want {
		t.Fatalf("match = %+v\nwant  %+v", match, want)
	}
}

func TestSearchTitleRangeIsRelativeToTitle(t *testing.T) {
	list := []issues.Issue{newIssue(1, "Fix the crash", "crash log", issues.Open)}

	results := mustSearch(t, list, "crash", Options{})

	got := numbers(results)
	if !slices.Equal(got, []string{"1/title", "1/body"}) {
		t.Fatalf("matches = %v", got)
	}
	title := results.Matches[0].Range
	if title != (search.Range{StartLine: 1, StartColumn: 9, EndLine: 1, EndColumn: 14}) {
		t.Fatalf("title range = %+v", title)
	}
	body := results.Matches[1].Range
	if body != (search.Range{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 6}) {
		t.Fatalf("body range = %+v", body)
	}
}

func TestSearchTitleLineBreaksStayInTitle(t *testing.T) {
	list := []issues.Issue{newIssue(1, "first\nsecond", "", issues.Open)}

	results := mustSearch(t, list, "second", Options{})

	if len(results.Matches) != 1 || results.Matches[0].Field != FieldTitle {
		t.Fatalf("matches = %v", numbers(results))
	}
	if results.Matches[0].Issue.Title != "first second" {
		t.Fatalf("title = %q", results.Matches[0].Issue.Title)
	}
}

func TestSearchOrdersByNumber(t *testing.T) {
	list := []issues.Issue{
		newIssue(10, "bug", "", issues.Open),
		newIssue(9, "bug", "", issues.Open),
		newIssue(100, "bug", "", issues.Open),
	}

	got := numbers(mustSearch(t, list, "bug", Options{}))

	if !slices.Equal(got, []string{"9/title", "10/title", "100/title"}) {
		t.Fatalf("matches = %v", got)
	}
}

func TestSearchFilters(t *testing.T) {
	other := newIssue(1, "bug", "", issues.Open)
	other.Repository = "owner/other"
	elsewhere := newIssue(1, "bug", "", issues.Open)
	elsewhere.Source.ID = "gitlab"

	list := []issues.Issue{
		newIssue(1, "bug", "", issues.Open),
		newIssue(2, "bug", "", issues.Closed),
		other,
		elsewhere,
	}

	cases := map[string]struct {
		filter Filter
		want   int
	}{
		"all":        {Filter{}, 4},
		"repository": {Filter{Repository: "owner/name"}, 3},
		"source":     {Filter{Source: "github"}, 3},
		"both":       {Filter{Source: "github", Repository: "owner/name"}, 2},
		"open":       {Filter{State: issues.Open}, 3},
		"closed":     {Filter{Source: "github", State: issues.Closed}, 1},
	}
	for name, test := range cases {
		results := mustSearch(t, list, "bug", Options{Filter: test.filter})
		if len(results.Matches) != test.want {
			t.Errorf("%s: %d matches, want %d", name, len(results.Matches), test.want)
		}
	}
}

func TestSearchStateFilterCountsResultLimit(t *testing.T) {
	list := []issues.Issue{
		newIssue(1, "bug", "", issues.Closed),
		newIssue(2, "bug", "", issues.Closed),
		newIssue(3, "bug", "", issues.Open),
	}
	options := Options{Filter: Filter{State: issues.Open}, Limits: Limits{Results: 2}}

	results := mustSearch(t, list, "bug", options)

	if len(results.Matches) != 0 || !results.State.Truncated {
		t.Fatalf("results = %v, %+v", numbers(results), results.State)
	}
}

func TestSearchQueryLimitTruncates(t *testing.T) {
	list := []issues.Issue{
		newIssue(1, "bug", "", issues.Open),
		newIssue(2, "bug", "", issues.Closed),
		newIssue(3, "bug", "", issues.Open),
	}

	results, err := Search(context.Background(), list, search.Query{Text: "bug", Limit: 1},
		Options{Filter: Filter{State: issues.Open}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(numbers(results), []string{"1/title"}) || !results.State.Truncated {
		t.Fatalf("results = %v, %+v", numbers(results), results.State)
	}
}

func TestSearchSkipsBinaryIssues(t *testing.T) {
	list := []issues.Issue{
		newIssue(1, "bug", "nul \x00 byte", issues.Open),
		newIssue(2, "bug", "", issues.Open),
	}

	results := mustSearch(t, list, "bug", Options{})

	if !slices.Equal(numbers(results), []string{"2/title"}) || results.State.Skipped != 1 {
		t.Fatalf("results = %v, %+v", numbers(results), results.State)
	}
}

func TestSearchStoresNothing(t *testing.T) {
	list := []issues.Issue{newIssue(1, "bug", "", issues.Open)}

	mustSearch(t, list, "bug", Options{})
	list[0].Title = "changed"

	if results := mustSearch(t, list, "bug", Options{}); len(results.Matches) != 0 {
		t.Fatalf("matches = %v", numbers(results))
	}
}

func TestSearchRepositoryUsesReader(t *testing.T) {
	listed := newIssue(4, "bug", "", issues.Closed)
	listed.Repository = "Owner/Name"
	reader := &fakeReader{issues: []issues.Issue{listed}}

	results, err := SearchRepository(context.Background(), reader, testRepository(),
		search.Query{Text: "bug"}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := issues.ListOptions{Source: "github", Repository: "owner/name", State: issues.StateAll}
	if len(reader.asked) != 1 || reader.asked[0].Source != want.Source ||
		reader.asked[0].Repository != want.Repository || reader.asked[0].State != want.State {
		t.Fatalf("asked = %+v", reader.asked)
	}
	if len(results.Matches) != 1 || results.Matches[0].Issue.Repository != "owner/name" {
		t.Fatalf("matches = %+v", results.Matches)
	}
}

func TestSearchRepositoryReaderErrors(t *testing.T) {
	failure := errors.New("forge down")
	reader := &fakeReader{err: failure}

	_, err := SearchRepository(context.Background(), reader, testRepository(), search.Query{Text: "bug"}, Options{})
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, err := SearchRepository(ctx, reader, testRepository(), search.Query{Text: "bug"}, Options{})
	if err != nil || !results.State.Canceled {
		t.Fatalf("canceled = %+v, %v", results.State, err)
	}
}

func TestSearchRejectsBadRequests(t *testing.T) {
	hashed := newIssue(1, "bug", "", issues.Open)
	hashed.Repository = "owner/name#1"
	unnumbered := newIssue(0, "bug", "", issues.Open)
	unknown := newIssue(1, "bug", "", "merged")

	cases := map[string]struct {
		list    []issues.Issue
		options Options
	}{
		"repository with #": {[]issues.Issue{hashed}, Options{}},
		"no number":         {[]issues.Issue{unnumbered}, Options{}},
		"unknown state":     {[]issues.Issue{unknown}, Options{}},
		"duplicate":         {[]issues.Issue{newIssue(1, "a", "", issues.Open), newIssue(1, "b", "", issues.Open)}, Options{}},
		"filter state":      {nil, Options{Filter: Filter{State: "merged"}}},
		"filter repository": {nil, Options{Filter: Filter{Repository: "a#b"}}},
	}
	for name, test := range cases {
		_, err := Search(context.Background(), test.list, search.Query{Text: "bug"}, test.options)
		if !errors.Is(err, search.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	if _, err := Search(context.Background(), nil, search.Query{Text: "a\nb"}, Options{}); !errors.Is(err, search.ErrInvalid) {
		t.Errorf("query: err = %v", err)
	}
}

func TestSearchErrorsHoldNoIssueText(t *testing.T) {
	secret := "secret-text"
	duplicate := []issues.Issue{newIssue(1, secret, secret, issues.Open), newIssue(1, secret, secret, issues.Open)}
	unknown := newIssue(2, secret, secret, issues.State(secret))

	for _, list := range [][]issues.Issue{duplicate, {unknown}} {
		_, err := Search(context.Background(), list, search.Query{Text: "bug"}, Options{})
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("err = %v", err)
		}
	}
}

func TestSearchLimits(t *testing.T) {
	longTitle := newIssue(2, strings.Repeat("t", MaxTitleBytes+1), "bug", issues.Open)
	longURL := newIssue(3, "bug", "", issues.Open)
	longURL.WebURL = strings.Repeat("u", MaxURLBytes+1)
	list := []issues.Issue{
		newIssue(1, "bug", strings.Repeat("x", 10), issues.Open),
		longTitle,
		longURL,
		newIssue(4, "bug", strings.Repeat("x", 11), issues.Open),
	}

	results := mustSearch(t, list, "bug", Options{Limits: Limits{BodyBytes: 10}})

	if !slices.Equal(numbers(results), []string{"1/title"}) || results.Issues.TooLarge != 3 {
		t.Fatalf("results = %v, %+v", numbers(results), results.Issues)
	}

	_, err := Search(context.Background(), list, search.Query{Text: "bug"}, Options{Limits: Limits{Issues: 3}})
	if !errors.Is(err, search.ErrTooLarge) {
		t.Fatalf("issues: err = %v", err)
	}

	reader := &fakeReader{issues: list}
	_, err = SearchRepository(context.Background(), reader, testRepository(), search.Query{Text: "bug"},
		Options{Limits: Limits{Issues: 3}})
	if !errors.Is(err, search.ErrTooLarge) {
		t.Fatalf("repository issues: err = %v", err)
	}
}

func TestDefaultLimits(t *testing.T) {
	if got := (Limits{}).withDefaults(DefaultLimits()); got != DefaultLimits() {
		t.Fatalf("limits = %+v", got)
	}
	partial := Limits{Results: 5}.withDefaults(DefaultSyncLimits())
	if partial.Results != 5 || partial.Issues != DefaultSyncLimits().Issues {
		t.Fatalf("limits = %+v", partial)
	}
}
