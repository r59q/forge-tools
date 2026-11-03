package review

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// devNull is the patch path of a file side that does not exist.
const devNull = "/dev/null"

func filePath(v string) bool {
	if v == "" || strings.HasPrefix(v, "/") || strings.ContainsAny(v, "\x00\r\n\\") {
		return false
	}

	for _, p := range strings.Split(v, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}

	return utf8.ValidString(v)
}

func headerPath(v string) (string, error) {
	v = strings.SplitN(v, "\t", 2)[0]
	if strings.HasPrefix(v, `"`) {
		s, e := strconv.Unquote(v)
		if e != nil {
			return "", ErrInvalid
		}

		v = s
	}

	if v == devNull {
		return "", nil
	}
	if strings.HasPrefix(v, "a/") || strings.HasPrefix(v, "b/") {
		v = v[2:]
	}
	if !filePath(v) {
		return "", ErrInvalid
	}

	return v, nil
}

func gitPaths(v string) (string, string, error) {
	left, right, err := splitGitPaths(v)
	if err != nil {
		return "", "", err
	}

	a, e := headerPath(left)
	if e != nil {
		return "", "", e
	}

	b, e := headerPath(right)

	return a, b, e
}

// splitGitPaths splits a "diff --git" header into its raw old and new paths.
// Git quotes paths containing controls or non-ASCII. Ordinary spaces remain
// unquoted. A quoted path fixes the boundary; multiple possible boundaries
// between unquoted paths are rejected instead of guessing file identities.
func splitGitPaths(v string) (string, string, error) {
	if strings.HasPrefix(v, `"`) {
		return splitQuotedPath(v)
	}

	// Git quotes literal double quotes, so this is the destination's start.
	if left, right, ok := strings.Cut(v, ` "b/`); ok {
		if strings.ContainsRune(left, '"') {
			return "", "", ErrInvalid
		}

		return left, `"b/` + right, nil
	}

	if strings.Count(v, " b/") != 1 || strings.ContainsRune(v, '"') {
		return "", "", ErrInvalid
	}

	left, right, _ := strings.Cut(v, " b/")

	return left, "b/" + right, nil
}

func splitQuotedPath(v string) (string, string, error) {
	end := closingQuote(v)
	if end >= len(v) {
		return "", "", ErrInvalid
	}

	return v[:end+1], strings.TrimPrefix(v[end+1:], " "), nil
}

// closingQuote returns the index of the quote ending v's leading quoted
// string, or at least len(v) when it is unterminated.
func closingQuote(v string) int {
	end := 1
	for end < len(v) {
		if v[end] == '\\' {
			end += 2
			continue
		}

		if v[end] == '"' {
			return end
		}

		end++
	}

	return end
}

// escapePath escapes each segment of a slash-separated file path for a URL.
func escapePath(p string) string {
	segments := strings.Split(p, "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}

	return strings.Join(segments, "/")
}
