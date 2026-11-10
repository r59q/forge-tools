// Package checkouts manages persistent Git working copies on one Unix host.
// Working copies share repository objects and are not security sandboxes.
package checkouts

import (
	"context"
	"errors"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

var (
	// ErrInvalid means an argument is malformed or required configuration is absent.
	ErrInvalid = errors.New("checkouts: invalid argument")
	// ErrNotFound means a checkout, repository or reference does not exist.
	ErrNotFound = errors.New("checkouts: not found")
	// ErrConflict means an operation would violate lifecycle or cleanup protections.
	ErrConflict = errors.New("checkouts: operation conflicts with checkout state")
	// ErrTooLarge means Git output exceeded the per-operation limit.
	ErrTooLarge = errors.New("checkouts: output limit exceeded")
)

// Repository identifies an existing local repository or a connector repository.
// Set LocalPath alone, or Source and Path together. LocalPath is host-local.
type Repository struct {
	LocalPath string `json:"local_path,omitempty"`
	Source    string `json:"source,omitempty"`
	Path      string `json:"path,omitempty"`
}

// Checkout describes a working copy independently of its implementation.
// Revision is the commit resolved at creation; Inspect reports the current one.
type Checkout struct {
	ID         string     `json:"id"`
	Repository Repository `json:"repository"`
	Reference  string     `json:"reference"`
	Revision   string     `json:"revision"`
	State      string     `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	Location   string     `json:"location"`
	Mode       string     `json:"mode"`
}

const (
	// Worktree shares Git metadata with the source and requires trusted writers.
	Worktree = "worktree"
	// Isolated keeps writable Git metadata separate from the managed cache.
	Isolated = "isolated"

	// Creating marks a checkout being prepared.
	Creating = "creating"
	// Ready marks a successfully prepared checkout.
	Ready = "ready"
	// Retained marks a checkout explicitly kept by its consumer.
	Retained = "retained"
	// Removing marks a checkout whose removal has started.
	Removing = "removing"
	// Failed marks an unsuccessful or interrupted operation; files may remain.
	Failed = "failed"
)

// Input selects a repository and branch, tag or commit. Empty Reference means HEAD.
// Mode defaults to Worktree. Isolated requires a connected repository.
type Input struct {
	Repository Repository
	Reference  string
	Mode       string
}

// File reports index and working-tree status using Git's porcelain status codes.
// OldPath is set for renames/copies. Untracked and ignored files use ?? and !!.
type File struct {
	Path     string
	OldPath  string
	Index    string
	Worktree string
}

// Status reports current HEAD, optional attached branch and changed files.
// Files includes ignored files so cleanup cannot silently discard them.
type Status struct {
	Revision string
	Branch   string
	Files    []File
}

// Diff separates index-versus-HEAD and worktree-versus-index binary patches.
// Untracked and ignored contents are not included; Inspect reports their paths.
type Diff struct{ Staged, Unstaged string }

// Record holds metadata and the implementation's backing Git directory.
// GitDirectory must never be exposed as the checkout's repository identity.
type Record struct {
	Checkout     Checkout `json:"checkout"`
	GitDirectory string   `json:"git_directory"`
}

// State is a complete metadata snapshot keyed by stable checkout ID.
type State struct {
	Checkouts map[string]Record `json:"checkouts"`
}

// Store persists isolated snapshots. Update must commit successful callbacks
// atomically, leave failed callbacks unchanged and serialize concurrent access.
// One Manager exclusively owns a store and root until Close.
type Store interface {
	Read(context.Context) (State, error)
	Update(context.Context, func(*State) error) error
}

// Resolve supplies current connector-authenticated Git access for a source ID.
// It is called on every connected creation; no credentials are stored here.
type Resolve func(context.Context, string) (*forgeauth.GitRemote, error)
