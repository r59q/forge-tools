package code

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// maxPathBytes caps file paths, as Git and the forges do.
const maxPathBytes = 4096

// maxRefBytes caps ref names.
const maxRefBytes = 1024

// Ref namespaces the library reads and writes.
const (
	headsPrefix = "refs/heads/"
	tagsPrefix  = "refs/tags/"
)

// cleanRepo checks a repository path with forgerest.CleanPath.
func cleanRepo(repo string) (string, error) {
	p, ok := forgerest.CleanPath(repo)
	if !ok {
		return "", fmt.Errorf("%w: repository %q, want owner/name", ErrInvalid, repo)
	}
	return p, nil
}

// cleanFilePath checks a file path relative to the repository root, such
// as "docs/README.md". It rejects an empty path, a leading or trailing
// slash, empty, "." and ".." segments, control characters, invalid UTF-8
// and paths over 4096 bytes. The path is returned unchanged.
func cleanFilePath(p string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("%w: path %q: %s", ErrInvalid, p, why)
	}
	switch {
	case p == "":
		return bad("empty")
	case len(p) > maxPathBytes:
		return bad("too long")
	case !utf8.ValidString(p):
		return bad("invalid UTF-8")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return bad("control character")
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return bad("empty, . or .. segment")
		}
	}
	return p, nil
}

// cleanDir checks an optional directory path: "" is the root.
func cleanDir(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	return cleanFilePath(p)
}

// checkWritePath checks a path to write: cleanFilePath, and no ".git"
// segment in any case, including forms that some file systems treat as
// ".git", such as ".git." or ".GIT ".
func checkWritePath(p string) error {
	if _, err := cleanFilePath(p); err != nil {
		return err
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.EqualFold(strings.TrimRight(seg, ". "), ".git") {
			return fmt.Errorf("%w: path %q: .git segment", ErrInvalid, p)
		}
	}
	return nil
}

// checkRefName checks a full branch or tag name, such as
// "refs/heads/main", with checkRefNameGit. Beyond Git's rules it rejects
// "%" and a short name starting with "-".
func checkRefName(name string) error {
	if err := checkRefNameGit(name); err != nil {
		return err
	}
	short := strings.TrimPrefix(strings.TrimPrefix(name, headsPrefix), tagsPrefix)
	switch {
	case strings.HasPrefix(short, "-"):
		return fmt.Errorf("%w: ref %q: starts with -", ErrInvalid, name)
	case strings.Contains(name, "%"):
		return fmt.Errorf("%w: ref %q: contains '%%'", ErrInvalid, name)
	}
	return nil
}

// checkRefNameGit checks a full branch or tag name with a subset of git
// check-ref-format, and rejects other namespaces than refs/heads/ and
// refs/tags/.
func checkRefNameGit(name string) error {
	bad := func(why string) error {
		return fmt.Errorf("%w: ref %q: %s", ErrInvalid, name, why)
	}
	short, ok := strings.CutPrefix(name, headsPrefix)
	if !ok {
		short, ok = strings.CutPrefix(name, tagsPrefix)
	}
	switch {
	case !ok:
		return bad("want refs/heads/ or refs/tags/")
	case short == "":
		return bad("empty name")
	case len(name) > maxRefBytes:
		return bad("too long")
	case !utf8.ValidString(name):
		return bad("invalid UTF-8")
	case short == "@":
		return bad("is @")
	case strings.Contains(name, ".."), strings.Contains(name, "@{"):
		return bad("contains .. or @{")
	case strings.HasSuffix(name, "."):
		return bad("ends with .")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return bad(fmt.Sprintf("contains %q", r))
		}
	}
	for _, seg := range strings.Split(short, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".lock") {
			return bad("empty segment, or segment starting with . or ending with .lock")
		}
	}
	return nil
}

// checkOID checks a full object ID argument.
func checkOID(what string, id OID) error {
	if !id.Valid() {
		return fmt.Errorf("%w: %s %q, want a full lowercase object ID", ErrInvalid, what, id)
	}
	return nil
}

// escapePath escapes each segment of a checked file path or ref name with
// url.PathEscape and keeps the slashes, for GitHub and Forgejo URLs.
// GitLab wants the whole path as one segment: use url.PathEscape on it.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// reposPath is the GitHub and Forgejo API path of a repository,
// "/repos/{owner}/{name}", or false if repo does not have exactly two
// segments.
func reposPath(repo string) (string, bool) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || strings.Contains(name, "/") {
		return "", false
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), true
}

// parseOID checks an object ID read from a forge and lowercases it.
func parseOID(s string) (OID, error) {
	id := OID(strings.ToLower(s))
	if !id.Valid() {
		return "", fmt.Errorf("code: forge sent invalid object ID %q", s)
	}
	return id, nil
}
