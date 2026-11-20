// Package workspace prepares the directory an agent's client runs in: a
// scratch directory forge-agents owns, a path the caller names, or a
// forge-checkouts worktree.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Errors returned by workspaces.
var (
	ErrInvalid     = errors.New("invalid workspace")
	ErrUnavailable = errors.New("workspace kind not configured")
)

// Kind is where a workspace comes from.
type Kind string

// Workspace kinds.
const (
	// KindScratch is an empty directory forge-agents creates and removes.
	KindScratch Kind = "scratch"
	// KindPath is an existing directory the caller names; it is never removed.
	KindPath Kind = "path"
	// KindCheckout is a new forge-checkouts worktree.
	KindCheckout Kind = "checkout"
	// KindShared borrows the parent agent's directory.
	KindShared Kind = "shared"
)

// Repository identifies a forge-checkouts repository: a local path, or a
// path on a forge source.
type Repository struct {
	LocalPath string `json:"localPath,omitzero"`
	Source    string `json:"source,omitzero"`
	Path      string `json:"path,omitzero"`
}

// IsZero reports an unset repository.
func (r Repository) IsZero() bool {
	return r == Repository{}
}

// Spec asks for a workspace.
type Spec struct {
	Kind Kind
	// Path is the directory of a KindPath workspace.
	Path string
	// Repository and Reference describe a KindCheckout workspace. An empty
	// reference is the repository's default branch.
	Repository Repository
	Reference  string
}

// Workspace is a prepared directory.
type Workspace struct {
	Kind Kind   `json:"kind"`
	Path string `json:"path"`
	// Checkout is the forge-checkouts checkout ID of a KindCheckout
	// workspace.
	Checkout   string     `json:"checkout,omitzero"`
	Repository Repository `json:"repository,omitzero"`
	Reference  string     `json:"reference,omitzero"`
}

// Checkout is a worktree forge-checkouts created.
type Checkout struct {
	ID string
	// Path is where this host reaches the worktree.
	Path      string
	Reference string
}

// Checkouts creates and removes forge-checkouts worktrees.
type Checkouts interface {
	Create(ctx context.Context, repository Repository, reference string) (Checkout, error)
	// Remove removes a checkout; forge-checkouts refuses one with
	// uncommitted changes.
	Remove(ctx context.Context, id string) error
}

// Provider prepares and releases workspaces.
type Provider interface {
	Prepare(ctx context.Context, agentID string, spec Spec) (Workspace, error)
	// Release gives a workspace up when its agent is deleted. Scratch
	// directories are always removed; checkouts only when remove is set.
	Release(ctx context.Context, w Workspace, remove bool) error
}

// Local provides workspaces on this host.
type Local struct {
	// ScratchDir holds scratch directories.
	ScratchDir string
	// Roots are the directories KindPath workspaces must be inside; none
	// disables KindPath.
	Roots []string
	// Checkouts creates KindCheckout workspaces; nil disables them.
	Checkouts Checkouts
}

// Prepare creates or checks the workspace.
func (l Local) Prepare(ctx context.Context, agentID string, spec Spec) (Workspace, error) {
	switch spec.Kind {
	case KindScratch, "":
		dir := filepath.Join(l.ScratchDir, agentID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Workspace{}, fmt.Errorf("scratch workspace: %w", err)
		}
		return Workspace{Kind: KindScratch, Path: dir}, nil
	case KindPath:
		dir, err := l.inRoot(spec.Path)
		if err != nil {
			return Workspace{}, err
		}
		return Workspace{Kind: KindPath, Path: dir}, nil
	case KindCheckout:
		if l.Checkouts == nil {
			return Workspace{}, fmt.Errorf("%w: checkout", ErrUnavailable)
		}
		if spec.Repository.IsZero() {
			return Workspace{}, fmt.Errorf("%w: a checkout workspace needs a repository", ErrInvalid)
		}
		c, err := l.Checkouts.Create(ctx, spec.Repository, spec.Reference)
		if err != nil {
			return Workspace{}, err
		}
		return Workspace{Kind: KindCheckout, Path: c.Path, Checkout: c.ID, Repository: spec.Repository, Reference: c.Reference}, nil
	}
	return Workspace{}, fmt.Errorf("%w: kind %q", ErrInvalid, spec.Kind)
}

// Release removes a scratch directory, or a checkout when remove is set.
func (l Local) Release(ctx context.Context, w Workspace, remove bool) error {
	switch w.Kind {
	case KindScratch:
		if filepath.Dir(w.Path) != filepath.Clean(l.ScratchDir) {
			return fmt.Errorf("%w: scratch directory %q is outside %q", ErrInvalid, w.Path, l.ScratchDir)
		}
		if err := os.RemoveAll(w.Path); err != nil {
			return fmt.Errorf("remove scratch workspace: %w", err)
		}
	case KindCheckout:
		if !remove {
			return nil
		}
		if l.Checkouts == nil {
			return fmt.Errorf("%w: checkout", ErrUnavailable)
		}
		return l.Checkouts.Remove(ctx, w.Checkout)
	case KindPath, KindShared:
	}
	return nil
}

func (l Local) inRoot(path string) (string, error) {
	if len(l.Roots) == 0 {
		return "", fmt.Errorf("%w: path", ErrUnavailable)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path %q is not absolute", ErrInvalid, path)
	}
	dir, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: %q is not a directory", ErrInvalid, path)
	}
	for _, root := range l.Roots {
		r, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(r, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return dir, nil
		}
	}
	return "", fmt.Errorf("%w: %q is outside the allowed roots", ErrInvalid, path)
}
