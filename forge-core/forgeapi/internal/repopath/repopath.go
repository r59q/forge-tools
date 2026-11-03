// Package repopath checks repository paths for forgeauth and forgerest.
package repopath

import "strings"

// Clean checks a repository path, such as "owner/name" or the nested GitLab
// path "group/sub/project", and trims surrounding space and slashes. It
// needs an owner and a name, and rejects empty, "." and ".." segments,
// control characters, spaces and any of % ? # \.
func Clean(path string) (string, bool) {
	p := strings.Trim(strings.TrimSpace(path), "/")
	segments := strings.Split(p, "/")
	if len(segments) < 2 {
		return "", false
	}

	for _, seg := range segments {
		if !validSegment(seg) {
			return "", false
		}
	}

	return p, true
}

// validSegment reports whether seg is a usable path segment.
func validSegment(seg string) bool {
	if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, `%?#\`) {
		return false
	}

	for _, r := range seg {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}

	return true
}
