// Package review manages fixed code review snapshots and reviewer feedback.
// Connected subjects use caller-owned forge-connector sessions. Approvals are
// assessments; the forge or application decides whether they permit merging.
package review

import (
	"context"
	"errors"
	"time"
)

// ErrInvalid indicates malformed input or an unverifiable comment location.
var ErrInvalid = errors.New("review: invalid argument")

// ErrNotFound indicates a missing subject, review or thread.
var ErrNotFound = errors.New("review: not found")

// ErrConflict indicates a submitted review or a changed upstream revision.
var ErrConflict = errors.New("review: conflict")

// ErrUnsupported indicates an operation the adapter does not implement.
var ErrUnsupported = errors.New("review: unsupported capability")

// ErrTooLarge indicates a snapshot or response over the size limit.
var ErrTooLarge = errors.New("review: too large")

// Ref identifies a connected repository and optional native request number.
type Ref struct {
	Source     string `json:"source"`
	Repository string `json:"repository"`
	Number     int64  `json:"number,omitempty"`
}

// Revision contains exact commit IDs, never moving reference names.
// Target is the comparison start; Base is the diff base (GitLab base_sha).
type Revision struct {
	Base   string `json:"base"`
	Head   string `json:"head"`
	Target string `json:"target"`
}

// FileContext is optional complete text for one file on the old or new side.
type FileContext struct {
	Path    string `json:"path"`
	Side    string `json:"side"`
	Content string `json:"content"`
}

// Input selects patch, comparison or change_request. Patch subjects need no
// connection. From and To are resolved before a comparison is retrieved.
type Input struct {
	Kind    string        `json:"kind"`
	Ref     Ref           `json:"ref"`
	From    string        `json:"from,omitempty"`
	To      string        `json:"to,omitempty"`
	Patch   string        `json:"patch,omitempty"`
	Context []FileContext `json:"context,omitempty"`
}

// File describes a changed file; absent sides have an empty path.
type File struct {
	OldPath string `json:"oldPath"`
	Path    string `json:"path"`
	Binary  bool   `json:"binary"`
}

// Subject is an immutable snapshot. ID hashes its identity, revisions, patch
// and context. Completeness is supplied or unknown (upstream limits may apply).
type Subject struct {
	ID           string        `json:"id"`
	Kind         string        `json:"kind"`
	Ref          Ref           `json:"ref"`
	Revision     Revision      `json:"revision"`
	Patch        string        `json:"patch"`
	Context      []FileContext `json:"context"`
	Files        []File        `json:"files"`
	Completeness string        `json:"completeness"`
	CreatedAt    time.Time     `json:"createdAt"`
}

// Anchor identifies a file and old/new side in a subject. Start and End are
// inclusive one-based lines; both zero mean the whole file. Nil means general.
type Anchor struct {
	Path  string `json:"path"`
	Side  string `json:"side"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Comment is feedback from a caller-supplied reviewer identity.
type Comment struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

// Thread preserves its subject and original anchor across replies/resolution.
type Thread struct {
	ID        string    `json:"id"`
	SubjectID string    `json:"subjectId"`
	Anchor    *Anchor   `json:"anchor,omitempty"`
	Comments  []Comment `json:"comments"`
	Resolved  bool      `json:"resolved"`
}

// Review tracks one standalone assignment and its assessment. Status is
// requested, commented, approved or changes_requested. Storage is always local.
type Review struct {
	ID          string     `json:"id"`
	SubjectID   string     `json:"subjectId"`
	Reviewer    string     `json:"reviewer"`
	Storage     string     `json:"storage"`
	Status      string     `json:"status"`
	Body        string     `json:"body"`
	Threads     []Thread   `json:"threads"`
	CreatedAt   time.Time  `json:"createdAt"`
	SubmittedAt *time.Time `json:"submittedAt,omitempty"`
}

// State is the standalone store's complete transactional data.
type State struct {
	Subjects map[string]Subject `json:"subjects"`
	Reviews  map[string]Review  `json:"reviews"`
}

// Store persists snapshots and reviews. Read returns an isolated copy. Update
// serializes writers, invokes fn once on an isolated state, and atomically
// commits only if fn succeeds. Implementations must initialize both maps.
// Callbacks must not retain state or invoke Store methods.
type Store interface {
	Read(context.Context) (State, error)
	Update(context.Context, func(*State) error) error
}
