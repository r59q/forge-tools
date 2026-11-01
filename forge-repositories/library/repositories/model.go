// Package repositories reads repositories, branches and tags from GitHub,
// GitLab and Forgejo through one forge-agnostic API.
//
// A Client combines several sources, such as one user's GitHub and GitLab
// accounts. Each source is a forgeauth session opened against a forge, or
// against a forge-connector-proxy connection; only the base URL and token
// differ. Results say which source and forge they came from, so callers
// never pick a per-forge API.
//
//	c, err := repositories.New(
//		repositories.Source{ID: "github", Session: gh},
//		repositories.Source{ID: "gitlab", Session: gl},
//	)
//	repos, err := c.List(ctx, repositories.ListOptions{Owner: "mrbean"})
//
// The package only reads repositories and updates small metadata fields. It
// never creates, forks or deletes repositories, branches or tags.
package repositories

import (
	"errors"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Errors returned by Client methods. Forge errors are returned as
// *forgehttp.Error, so errors.Is works with forgehttp.ErrNotFound,
// forgehttp.ErrForbidden and the other forgehttp sentinels on every forge.
var (
	// ErrInvalid means an argument is malformed, such as an empty path.
	ErrInvalid = errors.New("repositories: invalid argument")
	// ErrUnknownSource means a Ref or ListOptions names no configured source.
	ErrUnknownSource = errors.New("repositories: unknown source")
	// ErrAmbiguous means a path without a source exists on several sources.
	// Set Ref.Source, or use Client.Find to get every match.
	ErrAmbiguous = errors.New("repositories: path exists on several sources")
)

// Source is one place to read repositories from: a forge account reached
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

// SourceInfo says where a repository was read from.
type SourceInfo struct {
	// ID is the Source.ID the repository was read from.
	ID string
	// Kind is the type of forge.
	Kind forgeauth.Kind
	// Host is the forge's web host, such as "gitlab.com", taken from the
	// repository's web URL. Through a proxy it still names the forge.
	Host string
}

// Visibility says who can see a repository.
type Visibility string

// Repository visibilities.
const (
	// Public repositories are visible to everyone.
	Public Visibility = "public"
	// Internal repositories are visible to every signed-in user of the forge.
	Internal Visibility = "internal"
	// Private repositories are visible to members only.
	Private Visibility = "private"
)

// Repository is the shared model of a repository on any forge.
type Repository struct {
	// Source says which source and forge the repository came from.
	Source SourceInfo
	// ID is the forge's own repository ID.
	ID string
	// Path is the full path within the forge, such as "owner/name" or a
	// nested GitLab path like "group/sub/project".
	Path string
	// Owner is the path without its last segment, such as "owner" or
	// "group/sub".
	Owner string
	// Name is the last path segment.
	Name string
	// Description is the repository's description, if any.
	Description string
	// DefaultBranch is the default branch name. It may be empty for an
	// empty repository.
	DefaultBranch string
	// Visibility says who can see the repository.
	Visibility Visibility
	// Archived reports whether the repository is read-only.
	Archived bool
	// Fork reports whether the repository is a fork.
	Fork bool
	// WebURL is the repository's page on the forge.
	WebURL string
	// CloneURL is the HTTPS clone URL.
	CloneURL string
	// SSHURL is the SSH clone URL.
	SSHURL string
	// CreatedAt is when the repository was created.
	CreatedAt time.Time
	// UpdatedAt is when the repository was last updated, as the forge
	// reports it.
	UpdatedAt time.Time
}

// Ref returns the reference that names r on its source.
func (r Repository) Ref() Ref { return Ref{Source: r.Source.ID, Path: r.Path} }

// Ref names a repository by path, optionally on one source.
type Ref struct {
	// Source is a Source.ID. If empty, every source is searched and the
	// path must exist on exactly one of them.
	Source string
	// Path is the repository's full path, such as "owner/name" or
	// "group/sub/project". Do not URL-encode it.
	Path string
}

// Branch is a branch of a repository.
type Branch struct {
	// Name is the branch name.
	Name string
	// SHA is the commit the branch points at.
	SHA string
	// Default reports whether this is the repository's default branch.
	Default bool
	// Protected reports whether the forge protects the branch.
	Protected bool
}

// Tag is a tag of a repository.
type Tag struct {
	// Name is the tag name.
	Name string
	// SHA is the commit the tag points at. For annotated tags this is the
	// tagged commit, not the tag object.
	SHA string
}

// ListOptions filter Client.List.
type ListOptions struct {
	// Owner keeps only repositories whose owner is this user, organization
	// or group, ignoring case. A GitLab group also matches its subgroups.
	// If empty, all repositories are kept.
	Owner string
	// Source reads only this Source.ID. If empty, every source is read.
	Source string
}

// Update changes repository metadata. Nil fields are left unchanged.
type Update struct {
	// Description sets the description. An empty string clears it.
	Description *string
	// DefaultBranch sets the default branch. It must name an existing
	// branch.
	DefaultBranch *string
}
