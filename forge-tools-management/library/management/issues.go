package management

import (
	"context"
	"time"

	"github.com/r59q/forge-tools/forge-issues/library/issues"
)

// IssueReader is implemented by issues.Client and remote adapters.
// Implementations must return the complete listing or an error.
type IssueReader interface {
	List(context.Context, issues.ListOptions) ([]issues.Issue, error)
}

// Issue is the issue metadata displayed by management. Bodies, URLs and
// credentials are deliberately excluded. Source, Repository and Number
// together identify an issue.
type Issue struct {
	Source     issues.SourceInfo
	Repository string
	Number     int64
	Title      string
	State      issues.State
	Author     string
	Assignees  []string
	Labels     []string
	Comments   int
	UpdatedAt  time.Time
}

// IssueQuery selects issues. The zero value lists the open issues assigned
// to each connected account.
type IssueQuery struct {
	// Scope is issues.Assigned or issues.Created. It applies only without a
	// Repository.
	Scope issues.Scope
	// State is issues.StateOpen, issues.StateClosed or issues.StateAll.
	State issues.StateFilter
	// Source limits the listing to one source ID. Empty asks every source.
	Source string
	// Repository lists one repository's issues, such as "owner/name".
	Repository string
}

// IssueListing distinguishes an absent capability from a successful empty
// result. It is valid only when ListIssues returns no error.
type IssueListing struct {
	Configured bool
	Issues     []Issue
}

// IssuesConfigured reports whether an issue capability is configured.
func (c *Client) IssuesConfigured() bool { return c.issues != nil }

// ListIssues returns every issue q selects, in the reader's source order. A
// malformed query is ErrInvalid. It never returns partial results after a
// failure.
func (c *Client) ListIssues(ctx context.Context, q IssueQuery) (IssueListing, error) {
	if err := ctx.Err(); err != nil {
		return IssueListing{}, err
	}
	if !validQuery(q) {
		return IssueListing{}, ErrInvalid
	}
	out := IssueListing{Configured: c.issues != nil, Issues: []Issue{}}
	if !out.Configured {
		return out, nil
	}
	values, err := c.issues.List(ctx, issues.ListOptions{Source: q.Source, Repository: q.Repository, State: q.State, Scope: q.Scope})
	if err != nil {
		if ctx.Err() != nil {
			return IssueListing{}, ctx.Err()
		}
		return IssueListing{}, ErrUnavailable
	}
	for _, i := range values {
		if i.Source.ID == "" || i.Source.Kind == "" || i.Repository == "" || i.Number <= 0 {
			return IssueListing{}, ErrUnavailable
		}
		entry := Issue{
			Source: i.Source, Repository: i.Repository, Number: i.Number, Title: i.Title,
			State: i.State, Author: i.Author.Login, Labels: i.Labels, Comments: i.Comments, UpdatedAt: i.UpdatedAt,
		}
		for _, a := range i.Assignees {
			entry.Assignees = append(entry.Assignees, a.Login)
		}
		out.Issues = append(out.Issues, entry)
	}
	return out, nil
}

// validQuery checks the enumerated fields. Paths and sources are checked by
// the reader.
func validQuery(q IssueQuery) bool {
	switch q.Scope {
	case "", issues.Assigned, issues.Created:
	default:
		return false
	}
	switch q.State {
	case "", issues.StateOpen, issues.StateClosed, issues.StateAll:
	default:
		return false
	}
	return q.Repository == "" || q.Scope == ""
}
