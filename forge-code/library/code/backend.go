package code

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// backend is one forge's REST adapter. Each method is one primitive read;
// the Client does revision parsing, validation, snapshot pinning, sorting,
// size caps and LFS detection on top.
//
// Rules for every method:
//   - All forge requests go through o.s with o.ctx (forgerest.Do, Open,
//     Each, Pages or Send). That session counts every request, redirects
//     included, against Limits.MaxCalls and fails with errBudget once
//     they run out. Use o.left to skip optional calls and exhausted to
//     turn a budget error into a Truncated result.
//   - Arguments are validated by the Client: repository paths with
//     forgerest.CleanPath (o.repo), file paths with cleanFilePath, ref
//     names with checkRefName, OIDs with OID.Valid. Escape path segments
//     with escapePath, or url.PathEscape the whole path for GitLab.
//   - Read only by OID: always pass the commit OID as ref= or sha=, since
//     some forge endpoints fall back to the default branch.
//   - A missing object, revision or path returns revisionMissing,
//     objectMissing or pathMissing (missing and isMissing map forge
//     404/400/422). Other forge errors are returned as is.
//   - Object IDs read from the forge go through parseOID, and commit
//     messages through commitMessage.
//   - Prefix every package-level identifier in an adapter file with the
//     forge name (githubX, gitlabX, forgejoX); shared helpers live in
//     backend.go and validate.go.
type backend interface {
	// capabilities reports what the forge supports. Writes is ignored;
	// the Client sets it from Source.Git.
	capabilities() Capabilities

	// defaultBranch returns the short name of the default branch, such
	// as "main", or "" if the repository has none.
	defaultBranch(o *op) (string, error)

	// commit returns the commit id. A missing commit, or an object that is
	// not a commit, is objectMissing(id).
	commit(o *op, id OID) (Commit, error)

	// ref looks up one ref by its full name, refs/heads/X or refs/tags/X,
	// matched exactly. Annotated tags are peeled into Ref.Commit; GitHub
	// peels at most 4 levels. A missing ref is (Ref{}, false, nil).
	ref(o *op, name string) (Ref, bool, error)

	// refs lists refs whose names start with prefix, which is exactly
	// "refs/heads/" or "refs/tags/", peeled like ref. It returns at most
	// limit (>= 1) refs in any order, and truncated if more exist. If the
	// call budget runs out it may return truncated with what it has, but
	// only if every returned Ref is complete; otherwise the error.
	refs(o *op, prefix string, limit int) (refs []Ref, truncated bool, err error)

	// history returns up to n (1 to 100) commits reachable from tip,
	// newest first in the forge's log order, only those that change path
	// if path is not "". pos is where the previous page ended, the zero
	// value for the first page; its meaning is up to the adapter, such
	// as a forge page number and an offset into that page. It returns the
	// position after the last returned commit and whether more may
	// follow. A path with no history gives an empty page, not an error.
	history(o *op, tip OID, path string, n int, pos historyPos) (commits []Commit, next historyPos, more bool, err error)

	// tree lists the directory at path ("" is the root) in commit. Entries
	// carry their full Path from the repository root. With recursive it
	// lists every entry below path, in any order; the Client sorts. It
	// stops at max entries, or when the call budget runs out (exhausted),
	// and then sets Truncated. Tree.ID is the directory's tree OID, or ""
	// if the forge does not report it. Commit and Path are set by the
	// Client. A missing path, or one that is not a directory, is
	// pathMissing(path).
	tree(o *op, commit OID, path string, recursive bool, max int) (Tree, error)

	// stat returns the entry at path (never "") in commit. A missing path
	// is pathMissing(path).
	stat(o *op, commit OID, path string) (Entry, error)

	// readFile opens the blob at path in commit without following
	// symlinks: a symlink's content is its target. It sets Blob.ID and
	// Blob.Size (-1 if unknown); the Client sets the rest and wraps the
	// body. A directory or submodule is ErrNotFile and a missing path
	// pathMissing(path). If the size is known up front and over
	// o.limits.MaxBlobBytes, it may return ErrTooLarge without fetching.
	readFile(o *op, commit OID, path string) (*Reader, error)

	// readBlob opens the blob id like readFile. A missing blob is
	// objectMissing(id).
	readBlob(o *op, id OID) (*Reader, error)

	// mergeBase returns the best common ancestor of two different,
	// existing commits. It returns errNoMergeBase if they share no
	// history, and ErrUnsupported if it cannot pick one base.
	mergeBase(o *op, a, b OID) (OID, error)
}

// commitMessage drops one trailing newline from a commit message. GitHub
// strips it, so every forge drops it to report the same message.
func commitMessage(m string) string { return strings.TrimSuffix(m, "\n") }

// historyPos is an adapter-defined position in a history listing. Both
// fields are within 0 to maxCursorPage and 0 to maxCursorOffset when they
// come from a cursor.
type historyPos struct {
	Page   int
	Offset int
}

// backends holds the supported adapters by kind.
var backends = map[forgeauth.Kind]backend{
	forgeauth.GitHub:  githubBackend{},
	forgeauth.GitLab:  gitlabBackend{},
	forgeauth.Forgejo: forgejoBackend{},
}

// op is the context of one Client call: its session, repository, limits,
// call budget and tree memo. Adapters get it with every call. It is safe
// for concurrent use.
type op struct {
	ctx context.Context
	// s is the source's session, with every request counted against
	// limits.MaxCalls.
	s *forgeauth.Session
	// repo is the checked repository path, such as "owner/name".
	repo   string
	limits Limits
	calls  atomic.Int64

	mu   sync.Mutex
	memo map[string]any
}

// newOp returns an op for one call on repo over s.
func newOp(ctx context.Context, s *forgeauth.Session, repo string, l Limits) *op {
	o := &op{ctx: ctx, repo: repo, limits: l, memo: map[string]any{}}
	c := *s.Client
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	c.Transport = &countingTransport{o: o, next: next}
	o.s = &forgeauth.Session{Kind: s.Kind, BaseURL: s.BaseURL, Client: &c}
	return o
}

// spend counts one forge call, or returns errBudget if none are left.
// The session counts HTTP requests itself; use spend only for work that
// does not go through it, such as in tests.
func (o *op) spend() error {
	if o.calls.Add(1) > int64(o.limits.MaxCalls) {
		return errBudget
	}
	return nil
}

// left returns how many forge calls the op may still make.
func (o *op) left() int {
	return max(0, o.limits.MaxCalls-int(o.calls.Load()))
}

// cached returns the value stored under key, calling load to fill it on
// first use. Errors are not stored. Keys are chosen by the adapter, such
// as "tree:" + OID; values live for one Client call only.
func cached[T any](o *op, key string, load func() (T, error)) (T, error) {
	o.mu.Lock()
	v, ok := o.memo[key]
	o.mu.Unlock()
	if ok {
		return v.(T), nil
	}
	t, err := load()
	if err != nil {
		return t, err
	}
	o.mu.Lock()
	o.memo[key] = t
	o.mu.Unlock()
	return t, nil
}

// countingTransport counts requests against an op's budget.
type countingTransport struct {
	o    *op
	next http.RoundTripper
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.o.spend(); err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	return t.next.RoundTrip(req)
}
