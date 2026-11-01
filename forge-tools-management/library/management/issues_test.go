package management

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-issues/library/issues"
)

type issueReaderFunc func(context.Context, issues.ListOptions) ([]issues.Issue, error)

func (f issueReaderFunc) List(ctx context.Context, o issues.ListOptions) ([]issues.Issue, error) {
	return f(ctx, o)
}

func TestIssueListing(t *testing.T) {
	updated := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	issue := issues.Issue{
		Source: issues.SourceInfo{ID: "personal", Kind: forgeauth.GitHub, Host: "github.com"}, Repository: "group/sub/repo", Number: 7,
		Title: "Crash", Body: "backend-secret", State: issues.Open, Author: issues.User{Login: "ann", WebURL: "https://github.com/ann"},
		Assignees: []issues.User{{Login: "bob"}, {Login: "cy"}}, Labels: []string{"bug"}, Comments: 3, WebURL: "https://github.com/x", UpdatedAt: updated,
	}
	other := issue
	other.Source = issues.SourceInfo{ID: "work", Kind: forgeauth.GitLab, Host: "gitlab.example"}
	for _, tc := range []struct {
		name       string
		reader     IssueReader
		query      IssueQuery
		configured bool
		count      int
		wantErr    error
	}{
		{name: "unconfigured"},
		{name: "empty", reader: issueReaderFunc(func(context.Context, issues.ListOptions) ([]issues.Issue, error) { return nil, nil }), configured: true},
		{name: "sources", reader: issueReaderFunc(func(_ context.Context, o issues.ListOptions) ([]issues.Issue, error) {
			if !reflect.DeepEqual(o, issues.ListOptions{Source: "work", Repository: "group/sub/repo", State: issues.StateAll}) {
				t.Fatalf("unexpected options: %+v", o)
			}
			return []issues.Issue{issue, other}, nil
		}), query: IssueQuery{Source: "work", Repository: "group/sub/repo", State: issues.StateAll}, configured: true, count: 2},
		{name: "scope", reader: issueReaderFunc(func(_ context.Context, o issues.ListOptions) ([]issues.Issue, error) {
			if o.Scope != issues.Created || o.State != issues.StateClosed {
				t.Fatalf("unexpected options: %+v", o)
			}
			return nil, nil
		}), query: IssueQuery{Scope: issues.Created, State: issues.StateClosed}, configured: true},
		{name: "failure discards partial data", reader: issueReaderFunc(func(context.Context, issues.ListOptions) ([]issues.Issue, error) {
			return []issues.Issue{issue}, errors.New("backend-secret")
		}), wantErr: ErrUnavailable},
		{name: "invalid identity", reader: issueReaderFunc(func(context.Context, issues.ListOptions) ([]issues.Issue, error) {
			return []issues.Issue{{Source: issue.Source, Repository: "a/b"}}, nil
		}), wantErr: ErrUnavailable},
		{name: "invalid scope", query: IssueQuery{Scope: "mentioned"}, wantErr: ErrInvalid},
		{name: "invalid state", query: IssueQuery{State: "opened"}, wantErr: ErrInvalid},
		{name: "scope with repository", query: IssueQuery{Scope: issues.Assigned, Repository: "a/b"}, wantErr: ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New(Config{Issues: tc.reader}).ListIssues(context.Background(), tc.query)
			if !errors.Is(err, tc.wantErr) || got.Configured != tc.configured || len(got.Issues) != tc.count {
				t.Fatalf("got %+v, %v", got, err)
			}
			if tc.count == 2 {
				want := Issue{
					Source: issue.Source, Repository: "group/sub/repo", Number: 7, Title: "Crash", State: issues.Open,
					Author: "ann", Assignees: []string{"bob", "cy"}, Labels: []string{"bug"}, Comments: 3, UpdatedAt: updated,
				}
				if !reflect.DeepEqual(got.Issues[0], want) || got.Issues[1].Source != other.Source {
					t.Fatalf("lost fields or source identity: %+v", got.Issues)
				}
			}
		})
	}
}

func TestIssuesConfigured(t *testing.T) {
	if New(Config{}).IssuesConfigured() {
		t.Fatal("unconfigured client reports issues")
	}
	reader := issueReaderFunc(func(context.Context, issues.ListOptions) ([]issues.Issue, error) { return nil, nil })
	if !New(Config{Issues: reader}).IssuesConfigured() {
		t.Fatal("configured client reports no issues")
	}
}

func TestIssueCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(Config{}).ListIssues(ctx, IssueQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	client := New(Config{Issues: issueReaderFunc(func(ctx context.Context, _ issues.ListOptions) ([]issues.Issue, error) {
		cancel()
		return nil, ctx.Err()
	})})
	if _, err := client.ListIssues(ctx, IssueQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
