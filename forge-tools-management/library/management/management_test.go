package management

import (
	"context"
	"errors"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
)

type readerFunc func(context.Context, repositories.ListOptions) ([]repositories.Repository, error)

func (f readerFunc) List(ctx context.Context, o repositories.ListOptions) ([]repositories.Repository, error) {
	return f(ctx, o)
}

func TestListing(t *testing.T) {
	repo := repositories.Repository{Source: repositories.SourceInfo{ID: "personal", Kind: forgeauth.GitHub, Host: "github.com"}, Path: "group/sub/repo", Visibility: repositories.Private}
	other := repo
	other.Source.ID = "work"
	other.Source.Kind = forgeauth.GitLab
	other.Source.Host = "gitlab.example"
	for _, tc := range []struct {
		name       string
		reader     RepositoryReader
		configured bool
		count      int
		wantErr    error
	}{
		{name: "unconfigured"},
		{name: "empty", reader: readerFunc(func(context.Context, repositories.ListOptions) ([]repositories.Repository, error) { return nil, nil }), configured: true},
		{name: "sources", reader: readerFunc(func(_ context.Context, o repositories.ListOptions) ([]repositories.Repository, error) {
			if o != (repositories.ListOptions{}) {
				t.Fatalf("unexpected filter: %+v", o)
			}
			return []repositories.Repository{repo, other}, nil
		}), configured: true, count: 2},
		{name: "failure discards partial data", reader: readerFunc(func(context.Context, repositories.ListOptions) ([]repositories.Repository, error) {
			return []repositories.Repository{repo}, errors.New("backend-secret")
		}), wantErr: ErrUnavailable},
		{name: "invalid identity", reader: readerFunc(func(context.Context, repositories.ListOptions) ([]repositories.Repository, error) {
			return []repositories.Repository{{Path: "a/b"}}, nil
		}), wantErr: ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New(tc.reader).ListRepositories(context.Background())
			if !errors.Is(err, tc.wantErr) || got.Configured != tc.configured || len(got.Repositories) != tc.count {
				t.Fatalf("got %+v, %v", got, err)
			}
			if tc.count == 2 && (got.Repositories[0].Source != repo.Source || got.Repositories[1].Source != other.Source || got.Repositories[1].Path != repo.Path || got.Repositories[0].Visibility != repositories.Private) {
				t.Fatalf("lost source identity or visibility: %+v", got)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(nil).ListRepositories(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	client := New(readerFunc(func(ctx context.Context, _ repositories.ListOptions) ([]repositories.Repository, error) {
		cancel()
		return nil, ctx.Err()
	}))
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if _, err := client.ListRepositories(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
