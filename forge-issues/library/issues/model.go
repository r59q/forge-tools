// Package issues reads and writes issues on GitHub, GitLab and Forgejo
// through one forge-agnostic API.
//
// A Client combines several sources, such as one user's GitHub and GitLab
// accounts. Each source is a forgeauth session opened against a forge, or
// against a forge-connector-proxy connection; only the base URL and token
// differ. Results say which source and forge they came from, so callers
// never pick a per-forge API.
//
//	c, err := issues.New(
//		issues.Source{ID: "github", Session: gh},
//		issues.Source{ID: "gitlab", Session: gl},
//	)
//	list, err := c.List(ctx, issues.ListOptions{Repository: "mrbean/teddy"})
//
// A repository is only a path, such as "owner/name" or the nested GitLab
// path "group/sub/project". Pull and merge requests are never returned.
// The package never deletes issues or comments.
package issues

import (
	"errors"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Errors returned by Client methods. Forge errors are returned as
// *forgehttp.Error, so errors.Is works with forgehttp.ErrNotFound,
// forgehttp.ErrForbidden and the other forgehttp sentinels on every forge.
var (
	// ErrInvalid means an argument is malformed, such as an empty title.
	ErrInvalid = errors.New("issues: invalid argument")
	// ErrUnknownSource means a Ref or ListOptions names no configured source.
	ErrUnknownSource = errors.New("issues: unknown source")
	// ErrAmbiguous means an issue without a source exists on several
	// sources. Set Ref.Source to pick one.
	ErrAmbiguous = errors.New("issues: issue exists on several sources")
)

// Source is one place to read issues from: a forge account reached
// directly or through a forge-connector-proxy connection.
type Source struct {
	// ID names the source, such as a connection ID. It must be unique within
	// a Client.
	ID string
	// Session is an open forgeauth session. Its Kind selects the forge API.
	// Its BaseURL is a forge API URL, or a proxy connection URL such as
	// http://127.0.0.1:8081/c/{connectionID}.
	Session *forgeauth.Session
}

// SourceInfo says where an issue was read from.
type SourceInfo struct {
	// ID is the Source.ID the issue was read from.
	ID string
	// Kind is the type of forge.
	Kind forgeauth.Kind
	// Host is the forge's web host, such as "gitlab.com", taken from the
	// issue's web URL. Through a proxy it still names the forge.
	Host string
}

// State says whether an issue is open or closed.
type State string

// Issue states.
const (
	// Open issues are still being worked on.
	Open State = "open"
	// Closed issues are done or rejected.
	Closed State = "closed"
)

// StateFilter selects issues by state when listing.
type StateFilter string

// State filters. The zero value lists open issues.
const (
	// StateOpen lists open issues. It is the default.
	StateOpen StateFilter = "open"
	// StateClosed lists closed issues.
	StateClosed StateFilter = "closed"
	// StateAll lists open and closed issues.
	StateAll StateFilter = "all"
)

// Scope selects which issues an account-wide listing returns.
type Scope string

// Listing scopes. The zero value lists assigned issues.
const (
	// Assigned lists issues assigned to the source's account. It is the
	// default.
	Assigned Scope = "assigned"
	// Created lists issues the source's account created.
	Created Scope = "created"
)

// User is a forge account.
type User struct {
	// Login is the user name, such as "mrbean".
	Login string
	// WebURL is the user's profile page.
	WebURL string
}

// Issue is the shared model of an issue on any forge.
type Issue struct {
	// Source says which source and forge the issue came from.
	Source SourceInfo
	// Repository is the full path of the issue's repository, such as
	// "owner/name" or "group/sub/project".
	Repository string
	// ID is the forge's own global issue ID.
	ID string
	// Number is the issue's number within its repository: the GitHub and
	// Forgejo number, or the GitLab iid.
	Number int64
	// Title is the issue's title.
	Title string
	// Body is the issue's description, if any.
	Body string
	// State says whether the issue is open or closed.
	State State
	// Author is the user who opened the issue.
	Author User
	// Assignees are the users the issue is assigned to.
	Assignees []User
	// Labels are the names of the issue's labels.
	Labels []string
	// Milestone is the title of the issue's milestone, if any.
	Milestone string
	// Comments is the number of comments, not counting GitLab system notes.
	Comments int
	// Locked reports whether only collaborators can comment.
	Locked bool
	// WebURL is the issue's page on the forge.
	WebURL string
	// CreatedAt is when the issue was opened.
	CreatedAt time.Time
	// UpdatedAt is when the issue was last updated, as the forge reports it.
	UpdatedAt time.Time
	// ClosedAt is when the issue was closed. It is zero for open issues.
	ClosedAt time.Time
}

// Ref returns the reference that names i on its source.
func (i Issue) Ref() Ref {
	return Ref{Source: i.Source.ID, Repository: i.Repository, Number: i.Number}
}

// Comment is a comment on an issue.
type Comment struct {
	// ID is the forge's own comment ID.
	ID string
	// Author is the user who wrote the comment.
	Author User
	// Body is the comment text.
	Body string
	// WebURL is the comment's place on the issue's page.
	WebURL string
	// CreatedAt is when the comment was written.
	CreatedAt time.Time
	// UpdatedAt is when the comment was last edited.
	UpdatedAt time.Time
}

// Ref names an issue by repository path and number, optionally on one
// source.
type Ref struct {
	// Source is a Source.ID. Reads may leave it empty: every source is
	// searched and the issue must exist on exactly one of them. Writes need
	// it.
	Source string
	// Repository is the repository's full path, such as "owner/name" or
	// "group/sub/project". Do not URL-encode it.
	Repository string
	// Number is the issue's number within the repository.
	Number int64
}

// RepositoryRef names a repository on one source.
type RepositoryRef struct {
	// Source is a Source.ID. It is required.
	Source string
	// Repository is the repository's full path. Do not URL-encode it.
	Repository string
}

// ListOptions filter Client.List.
type ListOptions struct {
	// Source reads only this Source.ID. If empty, every source is read.
	Source string
	// Repository lists one repository's issues. If empty, each source lists
	// its account's issues across repositories, selected by Scope.
	Repository string
	// State selects open, closed or all issues. The default is open.
	State StateFilter
	// Labels keeps only issues that have all of these label names.
	Labels []string
	// Scope selects the account-wide listing. The default is Assigned. It
	// must be empty when Repository is set.
	Scope Scope
}

// NewIssue is an issue to create.
type NewIssue struct {
	// Title is required.
	Title string
	// Body is the description.
	Body string
	// Labels are label names to set.
	Labels []string
	// Assignees are logins to assign.
	Assignees []string
}

// Update changes an issue. Nil fields are left unchanged; an empty slice
// clears the labels or assignees.
type Update struct {
	// Title sets the title. It must not be empty.
	Title *string
	// Body sets the description. An empty string clears it.
	Body *string
	// State opens or closes the issue.
	State *State
	// Labels replaces the label names.
	Labels *[]string
	// Assignees replaces the assigned logins.
	Assignees *[]string
}
