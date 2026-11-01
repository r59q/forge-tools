// Package management provides read-only inspection of optional forge-tools
// capabilities. Repository listing reuses forge-repositories without widening
// the visibility of its configured credentials.
package management

import (
	"context"
	"errors"

	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
)

// ErrUnavailable means a configured repository capability failed. Results are
// discarded, and upstream error details are never included.
var ErrUnavailable = errors.New("management: repository listing unavailable")

// RepositoryReader is implemented by repositories.Client and remote adapters.
// Implementations must return the complete listing or an error.
type RepositoryReader interface {
	List(context.Context, repositories.ListOptions) ([]repositories.Repository, error)
}

// Repository is the metadata displayed by management. URLs and credentials
// are deliberately excluded. Source and Path together identify an entry.
type Repository struct {
	Source      repositories.SourceInfo
	Path        string
	Description string
	Visibility  repositories.Visibility
}

// Listing distinguishes an absent capability from a successful empty result.
// It is valid only when ListRepositories returns no error.
type Listing struct {
	Configured   bool
	Repositories []Repository
}

// Client inspects the configured capabilities. It is safe for concurrent use
// when its RepositoryReader is safe for concurrent use.
type Client struct {
	repositories RepositoryReader
}

// New constructs a client. A nil reader means no repository capability is
// configured; no other tool is required.
func New(reader RepositoryReader) *Client { return &Client{repositories: reader} }

// ListRepositories returns every repository visible to the reader, in its
// source order. It never returns partial results after a failure.
func (c *Client) ListRepositories(ctx context.Context) (Listing, error) {
	if err := ctx.Err(); err != nil {
		return Listing{}, err
	}
	out := Listing{Configured: c.repositories != nil, Repositories: []Repository{}}
	if !out.Configured {
		return out, nil
	}
	values, err := c.repositories.List(ctx, repositories.ListOptions{})
	if err != nil {
		if ctx.Err() != nil {
			return Listing{}, ctx.Err()
		}
		return Listing{}, ErrUnavailable
	}
	for _, r := range values {
		if r.Source.ID == "" || r.Source.Kind == "" || r.Path == "" {
			return Listing{}, ErrUnavailable
		}
		out.Repositories = append(out.Repositories, Repository{
			Source: r.Source, Path: r.Path, Description: r.Description, Visibility: r.Visibility,
		})
	}
	return out, nil
}
