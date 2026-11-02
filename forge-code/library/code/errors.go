package code

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// Errors returned by Client methods. Forge failures are returned as
// *forgehttp.Error, so errors.Is works with forgehttp.ErrNotFound,
// forgehttp.ErrForbidden and the other forgehttp sentinels on every forge.
// A 404 means missing or not visible to the account; it is never reported
// as unauthorized.
var (
	// ErrInvalid means an argument is malformed, such as a bad revision,
	// path, ref name or option.
	ErrInvalid = errors.New("code: invalid argument")
	// ErrUnknownSource means a RepositoryRef names no configured source.
	ErrUnknownSource = errors.New("code: unknown source")
	// ErrRevisionNotFound means a revision or object does not exist.
	// Errors that wrap it also match forgehttp.ErrNotFound.
	ErrRevisionNotFound = errors.New("code: revision not found")
	// ErrPathNotFound means a path does not exist at the commit. Errors
	// that wrap it also match forgehttp.ErrNotFound.
	ErrPathNotFound = errors.New("code: path not found")
	// ErrNotFile means a path names a directory or submodule, not a file.
	ErrNotFile = errors.New("code: path is not a file")
	// ErrAmbiguous means a bare name matches both a tag and a branch.
	// Pass "refs/tags/X" or "refs/heads/X" instead.
	ErrAmbiguous = errors.New("code: ambiguous revision")
	// ErrTooLarge means content, a listing or the number of forge calls
	// exceeded a limit.
	ErrTooLarge = errors.New("code: size or call limit exceeded")
	// ErrUnsupported means the forge or source cannot do this.
	ErrUnsupported = errors.New("code: not supported by this forge or source")
	// ErrStale means a ref was not at the expected value, so nothing
	// changed. It does not wrap forgehttp.ErrConflict.
	ErrStale = errors.New("code: ref is not at the expected value")
	// ErrRejected means the forge refused an update, such as through a
	// hook, branch protection, token scope or a signed-commit rule.
	ErrRejected = errors.New("code: forge rejected the update")
)

// errBudget is returned once an operation has used up Limits.MaxCalls.
var errBudget = fmt.Errorf("%w: forge call limit reached", ErrTooLarge)

// errNoMergeBase means two commits share no history.
var errNoMergeBase = fmt.Errorf("%w: no common ancestor", ErrInvalid)

// notFoundError is a missing revision, object or path. It unwraps to kind
// and also matches forgehttp.ErrNotFound, without wrapping the forge's own
// error, which may be a 400 or 422.
type notFoundError struct {
	kind error
	what string
}

func (e *notFoundError) Error() string { return e.kind.Error() + ": " + e.what }

func (e *notFoundError) Unwrap() error { return e.kind }

func (e *notFoundError) Is(target error) bool { return target == forgehttp.ErrNotFound }

// revisionMissing is the error for a revision that does not exist.
func revisionMissing(rev string) error {
	return &notFoundError{kind: ErrRevisionNotFound, what: fmt.Sprintf("%q", rev)}
}

// objectMissing is the error for an object ID that does not exist.
func objectMissing(id OID) error {
	return &notFoundError{kind: ErrRevisionNotFound, what: "object " + string(id)}
}

// pathMissing is the error for a path that does not exist at a commit.
func pathMissing(path string) error {
	return &notFoundError{kind: ErrPathNotFound, what: fmt.Sprintf("%q", path)}
}

// isMissing reports whether err is a forge response meaning "no such
// object": 404, 400 or 422. Use it only where the library has validated
// the request, so a 400 or 422 cannot mean a malformed request.
func isMissing(err error) bool {
	var fe *forgehttp.Error
	if !errors.As(err, &fe) {
		return false
	}
	switch fe.StatusCode {
	case http.StatusNotFound, http.StatusBadRequest, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// missing returns notFound if isMissing(err), and err otherwise. notFound
// is usually revisionMissing, objectMissing or pathMissing.
func missing(err, notFound error) error {
	if isMissing(err) {
		return notFound
	}
	return err
}

// exhausted reports whether err comes from running out of Limits.MaxCalls.
// Listings use it to return a Truncated result instead of failing.
func exhausted(err error) bool { return errors.Is(err, errBudget) }
