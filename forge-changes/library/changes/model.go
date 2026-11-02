// Package changes manages proposed code changes on GitHub, GitLab and Forgejo.
// Callers supply authenticated sessions from forge-connector. Reviews and
// inline comments are outside this package; merging calls the forge directly.
package changes

import (
	"errors"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Errors identify invalid input, unsupported operations and failed guards.
var (
	// ErrInvalid means an argument is missing or malformed.
	ErrInvalid = errors.New("changes: invalid argument")
	// ErrUnknownSource means the connection ID is not configured.
	ErrUnknownSource = errors.New("changes: unknown source")
	// ErrUnsupported means the adapter cannot perform the operation.
	ErrUnsupported = errors.New("changes: unsupported operation")
	// ErrHeadChanged means an expected head or observed revision changed.
	ErrHeadChanged = errors.New("changes: head changed")
	// ErrMergeRejected means the proposal cannot be merged as requested.
	ErrMergeRejected = errors.New("changes: merge rejected")
	// ErrTooLarge means a response exceeds a library or known forge limit.
	ErrTooLarge = errors.New("changes: response too large")
)

// Source is a connection supplied by forge-connector, or an open session.
type Source struct {
	ID      string             // ID is a unique connection identifier.
	Session *forgeauth.Session // Session supplies authenticated API access.
}

// RepositoryRef names the target repository on a required source.
type RepositoryRef struct {
	Source     string // Source is a configured connection ID.
	Repository string // Repository is an unescaped owner/name or GitLab group/project path.
}

// Ref identifies a proposal by its target repository and native number.
type Ref struct {
	Source     string // Source is required for reads and writes.
	Repository string // Repository is the unescaped target repository path.
	Number     int64  // Number is the pull request number or GitLab iid, not its global ID.
}

// State preserves the distinction between a closed and a merged proposal.
type State string

// Proposal states.
const (
	// Open proposals may still be updated or merged.
	Open State = "open"
	// Closed proposals were closed without merging.
	Closed State = "closed"
	// Merged proposals were incorporated into their target branch.
	Merged State = "merged"
)

// User is the proposal or discussion author.
type User struct {
	Login  string // Login is the forge username.
	WebURL string // WebURL is the profile URL, if available.
}

// Revision identifies the commits observed for a proposal. Empty IDs mean
// unavailable, never a moving branch name. Target is the forge's observed
// target commit; on GitLab it is the diff version's start_sha.
type Revision struct {
	Head   string // Head is the source commit ID.
	Target string // Target is the target commit ID.
	Base   string // Base is the merge base, when the forge supplies it.
}

// ChangeRequest is a pull request or merge request in the shared model.
type ChangeRequest struct {
	Ref              Ref            // Ref identifies the proposal and its connection.
	Kind             forgeauth.Kind // Kind identifies the forge API.
	ID               string         // ID is the forge's global numeric ID as a string.
	Title            string         // Title is the native title, including any draft prefix.
	Description      string         // Description is the proposal's Markdown body.
	Author           User           // Author opened the proposal.
	SourceRepository string         // SourceRepository is empty if unavailable or deleted.
	SourceBranch     string         // SourceBranch is the branch being proposed.
	TargetBranch     string         // TargetBranch is the destination branch.
	Revision         Revision       // Revision contains resolved commit IDs.
	State            State          // State is open, closed or merged.
	Draft            bool           // Draft reports a work-in-progress proposal.
	WebURL           string         // WebURL is the proposal's forge page.
	CreatedAt        time.Time      // CreatedAt is when the proposal was opened.
	UpdatedAt        time.Time      // UpdatedAt is the last update reported by the forge.
	ClosedAt         time.Time      // ClosedAt is zero unless reported by the forge.
	MergedAt         time.Time      // MergedAt is zero unless merged.
}

// ListOptions filters proposals. Repository is required; Source may be empty
// to list that path across all configured sources. Missing repositories are
// skipped only when Source is empty. Results follow source and forge order.
type ListOptions struct {
	Source     string
	Repository string
	Author     string // Author is a username, compared ignoring case; empty means any author.
	State      string // State is open (default), closed, merged or all.
	Draft      *bool  // Draft, when set, selects draft or ready proposals.
}

// NewChange is a proposal to create between branches in the same repository.
type NewChange struct {
	Title        string
	Description  string
	SourceBranch string
	TargetBranch string
	Draft        bool
}

// Update changes proposal metadata or state. Nil means unchanged; an empty
// description clears it. SourceBranch is exposed but currently unsupported
// by all three forge APIs. Use SetDraft for draft transitions.
type Update struct {
	Title        *string
	Description  *string
	SourceBranch *string
	TargetBranch *string
	State        *State // State accepts open (reopen) or closed, never merged.
}

// Comment is a general discussion comment, excluding reviews and system notes.
type Comment struct {
	ID        string
	Author    User
	Body      string
	WebURL    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Commit is a proposal commit with the forge's full commit message.
type Commit struct {
	ID          string
	Message     string
	AuthorName  string
	AuthorEmail string
	CreatedAt   time.Time
}

// ChangedFile is forge-reported file metadata. Patch contains unified diff
// hunks without file headers. PatchUnavailable also covers binary files and
// forge limits; absence of text must not be interpreted as no change.
type ChangedFile struct {
	Path             string // Path is empty for a deleted file.
	OldPath          string // OldPath is empty for an added file.
	Status           string // Status is added, deleted, modified, renamed, copied or unknown.
	Patch            string
	PatchUnavailable bool
}

// Files is a file listing and the revision observed before and after reading
// it. A concurrent revision change fails with ErrHeadChanged.
type Files struct {
	Revision     Revision
	Files        []ChangedFile
	Completeness string // Completeness is complete, truncated or unknown (forge limits may apply).
}

// Diff is the raw unified diff supplied by the forge, with file headers.
// Completeness is unknown because upstream servers may impose diff limits.
type Diff struct {
	Revision     Revision
	Patch        string
	Completeness string
}

// Condition reports a forge merge condition. Unknown is distinct from met.
type Condition struct {
	Name   string // Name is state, draft, conflicts, reviews, checks or forge.
	Status string // Status is met, unmet or unknown.
	Detail string // Detail is the native status or why it is unavailable.
}

// Readiness is advisory. The forge enforces all conditions at merge time.
type Readiness struct {
	Revision   Revision
	Status     string // Status is ready, blocked or unknown.
	Conditions []Condition
}

// MergeOptions selects a merge method and optional atomic head guard.
type MergeOptions struct {
	Method       string // Method is merge, squash, rebase, rebase-merge, fast-forward-only or default.
	ExpectedHead string // ExpectedHead is a full lowercase commit ID; empty disables the caller's guard.
}

// MergeResult confirms a synchronous merge. It never represents auto-merge.
type MergeResult struct {
	Merged bool
	Commit string // Commit is the resulting commit ID, when the forge supplies it.
}

// Capabilities describes adapter operations; permissions and repository rules
// may restrict them further. GitLab default follows project merge settings.
type Capabilities struct {
	UpdateSourceBranch bool
	DraftTransition    bool
	MergeMethods       []string
	ExpectedHead       bool
}
