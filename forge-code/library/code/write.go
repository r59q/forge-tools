package code

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitwire"
)

// maxParents caps the parents of one commit.
const maxParents = 100

// CreateCommit makes a commit from n and pushes it, moving n.Ref from
// n.Old to the new commit in the same push. Changes apply to the tree of
// n.Parents[0], or to an empty tree for a root commit. If n.Ref is not at
// n.Old, nothing changes and the error wraps ErrStale. Deleting a missing
// path wraps ErrPathNotFound. A forge refusal, such as branch protection,
// wraps ErrRejected. Trees the library cannot rebuild exactly, and
// SHA-256 repositories, give ErrUnsupported, as does a source without a
// Git remote. If the connection fails after the push is sent and the
// result cannot be told, the error wraps neither ErrStale nor
// ErrRejected; read n.Ref before retrying. Commits are unsigned.
func (c *Client) CreateCommit(ctx context.Context, r RepositoryRef, n NewCommit) (Commit, error) {
	if err := c.checkCommit(n); err != nil {
		return Commit{}, err
	}

	s, o, err := c.start(ctx, r)
	if err != nil {
		return Commit{}, err
	}
	if s.Git == nil {
		return Commit{}, s.wrap(fmt.Errorf("%w: source has no Git remote", ErrUnsupported))
	}

	cm, err := createCommit(o, s, n)

	return cm, s.wrap(err)
}

// UpdateRefs creates, moves and deletes refs in one atomic push: either
// all updates apply or none. If any ref is not at its Old value, nothing
// changes and the error wraps ErrStale. A forge refusal, such as a hook
// or a missing New object, wraps ErrRejected. Several updates need a
// forge that supports atomic pushes, and deletes one that allows them,
// otherwise ErrUnsupported. If the connection fails after the push is
// sent and the result cannot be told, the error wraps neither ErrStale
// nor ErrRejected; read the refs before retrying.
func (c *Client) UpdateRefs(ctx context.Context, r RepositoryRef, u ...RefUpdate) error {
	if err := checkRefUpdates(u); err != nil {
		return err
	}

	s, o, err := c.start(ctx, r)
	if err != nil {
		return err
	}
	if s.Git == nil {
		return s.wrap(fmt.Errorf("%w: source has no Git remote", ErrUnsupported))
	}

	cmds := make([]gitwire.Command, len(u))
	for i, up := range u {
		old, err := sha1ID(up.Old)
		if err != nil {
			return s.wrap(err)
		}

		nw, err := sha1ID(up.New)
		if err != nil {
			return s.wrap(err)
		}

		cmds[i] = gitwire.Command{Name: up.Name, Old: old, New: nw}
	}

	url, err := s.Git.URL(o.repo)
	if err != nil {
		return s.wrap(err)
	}

	return s.wrap(push(ctx, s.Git, url, cmds, nil))
}

// sha1ID converts a checked OID. SHA-256 IDs give ErrUnsupported.
func sha1ID(id OID) (gitobj.ID, error) {
	if len(id) != 40 {
		return gitobj.ID{}, fmt.Errorf("%w: SHA-256 object ID %s", ErrUnsupported, id)
	}

	return gitobj.ParseID(string(id))
}

// createCommit builds n on top of its first parent's tree and pushes it.
func createCommit(o *op, s *source, n NewCommit) (Commit, error) {
	old, err := sha1ID(n.Old)
	if err != nil {
		return Commit{}, err
	}

	parents := make([]gitobj.ID, len(n.Parents))
	for i, p := range n.Parents {
		if parents[i], err = sha1ID(p); err != nil {
			return Commit{}, err
		}
	}

	url, err := s.Git.URL(o.repo)
	if err != nil {
		return Commit{}, err
	}

	e := &editor{o: o, b: s.forge, objs: map[gitobj.ID]gitobj.Object{}}
	e.root = &editTree{entries: map[string]gitobj.TreeEntry{}, kids: map[string]*editTree{}}
	if len(n.Parents) > 0 {
		e.base = n.Parents[0]
		rootID, err := baseTree(o, s, url, n.Parents)
		if err != nil {
			return Commit{}, err
		}
		if e.root, err = e.load("", rootID); err != nil {
			return Commit{}, err
		}
	}

	if err := e.apply(n.Changes); err != nil {
		return Commit{}, err
	}

	tree, err := e.write(e.root)
	if err != nil {
		return Commit{}, err
	}

	author, committer := signatures(n, time.Now().UTC())
	data, err := gitobj.EncodeCommit(gitobj.Commit{
		Tree:      tree,
		Parents:   parents,
		Author:    gitobj.Signature(author),
		Committer: gitobj.Signature(committer),
		Message:   n.Message,
	})
	if err != nil {
		return Commit{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	commit := gitobj.Object{Type: gitobj.TypeCommit, Data: data}
	id := commit.ID()
	e.objs[id] = commit

	cmd := gitwire.Command{Name: n.Ref, Old: old, New: id}
	if err := push(o.ctx, s.Git, url, []gitwire.Command{cmd}, packOrder(e.objs)); err != nil {
		return Commit{}, err
	}

	return pushedCommit(id, tree, data, n.Parents)
}

// signatures returns n's author and committer, with now for times not
// set. A zero committer is the author.
func signatures(n NewCommit, now time.Time) (author, committer Signature) {
	author, committer = n.Author, n.Committer
	if author.When.IsZero() {
		author.When = now
	}
	if committer == (Signature{}) {
		committer = author
	} else if committer.When.IsZero() {
		committer.When = now
	}

	return author, committer
}

// packOrder returns objs with blobs first, then trees, then commits, each
// sorted by ID, so the pack is the same for the same commit.
func packOrder(objs map[gitobj.ID]gitobj.Object) []gitobj.Object {
	rank := map[gitobj.Type]int{gitobj.TypeBlob: 0, gitobj.TypeTree: 1, gitobj.TypeCommit: 2}
	keys := slices.SortedFunc(maps.Keys(objs), func(a, b gitobj.ID) int {
		if d := rank[objs[a].Type] - rank[objs[b].Type]; d != 0 {
			return d
		}

		return bytes.Compare(a[:], b[:])
	})

	out := make([]gitobj.Object, len(keys))
	for i, k := range keys {
		out[i] = objs[k]
	}

	return out
}

// pushedCommit returns the Commit for the pushed commit id, encoded as
// data.
func pushedCommit(id, tree gitobj.ID, data []byte, parents []OID) (Commit, error) {
	dc, err := gitobj.DecodeCommit(data)
	if err != nil {
		return Commit{}, err
	}

	out := Commit{
		ID:        OID(id.String()),
		Tree:      OID(tree.String()),
		Parents:   slices.Clone(parents),
		Author:    Signature(dc.Author),
		Committer: Signature(dc.Committer),
		Message:   commitMessage(dc.Message),
	}
	if out.Parents == nil {
		out.Parents = []OID{}
	}

	return out, nil
}

// baseTree checks that every parent exists and returns the root tree ID
// of the first one. On forges that do not report tree IDs it fetches the
// commit object over Git.
func baseTree(o *op, s *source, url string, parents []OID) (gitobj.ID, error) {
	first, err := s.forge.commit(o, parents[0])
	if err != nil {
		return gitobj.ID{}, err
	}

	for _, p := range parents[1:] {
		if _, err := s.forge.commit(o, p); err != nil {
			return gitobj.ID{}, err
		}
	}

	if first.Tree != "" {
		return sha1ID(first.Tree)
	}

	pid, _ := sha1ID(parents[0])
	data, err := gitwire.FetchCommit(o.ctx, s.Git.Client, url, pid)
	if err != nil {
		return gitobj.ID{}, fmt.Errorf("fetch parent %s for its tree ID: %w", pid, gitError(err))
	}

	pc, err := gitobj.DecodeCommit(data)
	if err != nil {
		return gitobj.ID{}, fmt.Errorf("%w: parent %s: %w", ErrUnsupported, pid, err)
	}

	return pc.Tree, nil
}
