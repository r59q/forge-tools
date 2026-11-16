package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
)

// Target limits: the longest source, repository path and reference, and the
// most segments and longest segment of a repository path.
const (
	MaxSourceBytes     = 128
	MaxRepositoryBytes = 256
	MaxReferenceBytes  = 256

	maxSegments     = 16
	maxSegmentBytes = 100
)

// repositoryPrefix starts the name of every index IndexRepository makes.
// IndexCheckout refuses such names, so that the two never share an index.
const repositoryPrefix = "repo-"

// Target is a repository reference: a forge-connector source, a repository
// path such as "owner/name", and a branch, tag or commit. An empty
// reference is the default branch.
type Target struct {
	Source     string `json:"source"`
	Repository string `json:"repository"`
	Reference  string `json:"reference"`
}

// target validates a requested target, naming the default branch "".
func target(t *pb.RepositoryTarget) (Target, error) {
	out := Target{Source: t.GetSource(), Repository: t.GetRepository(), Reference: t.GetReference()}
	if out.Reference == "HEAD" {
		out.Reference = ""
	}

	valid := validSource(out.Source) && validRepository(out.Repository) && validReference(out.Reference)
	if !valid {
		return Target{}, errBadRequest
	}
	return out, nil
}

// index is the name of the target's index: a hash of its fields, so that
// no field reaches the name or a path.
func (t Target) index() string {
	sum := sha256.Sum256([]byte(t.Source + "\x00" + t.Repository + "\x00" + t.Reference))
	return repositoryPrefix + hex.EncodeToString(sum[:20])
}

// record is the document ID that stores the target in its index.
func (t Target) record() string {
	b, err := json.Marshal(t)
	if err != nil {
		panic(err) // A struct of strings always encodes.
	}
	return string(b)
}

func (t Target) proto() *pb.RepositoryTarget {
	return &pb.RepositoryTarget{Source: t.Source, Repository: t.Repository, Reference: t.Reference}
}

// parseTarget decodes a target record.
func parseTarget(record string) (Target, error) {
	var t Target
	if err := json.Unmarshal([]byte(record), &t); err != nil {
		return Target{}, err
	}
	return t, nil
}

func validSource(source string) bool {
	return source != "" && len(source) <= MaxSourceBytes && only(source, idChar)
}

func validRepository(path string) bool {
	segments := strings.Split(path, "/")
	if len(path) > MaxRepositoryBytes || len(segments) < 2 || len(segments) > maxSegments {
		return false
	}

	for _, segment := range segments {
		valid := segment != "" && len(segment) <= maxSegmentBytes &&
			segment != "." && segment != ".." && only(segment, pathChar)
		if !valid {
			return false
		}
	}
	return true
}

// validReference reports whether ref is empty or a Git reference name, as
// git check-ref-format allows it with --allow-onelevel, that does not start
// with "-". A commit ID is such a name.
func validReference(ref string) bool {
	if ref == "" {
		return true
	}

	forbidden := strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "/") ||
		strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") ||
		strings.Contains(ref, "/.") || strings.HasPrefix(ref, ".") || ref == "@"
	if forbidden || len(ref) > MaxReferenceBytes {
		return false
	}
	return only(ref, refChar)
}

func only(s string, allowed func(rune) bool) bool {
	for _, c := range s {
		if !allowed(c) {
			return false
		}
	}
	return true
}

// idChar is a character of a forge-connector connection ID.
func idChar(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

func pathChar(c rune) bool {
	return idChar(c) || c == '.'
}

// refChar is a character Git allows in a reference name: printable ASCII
// other than space and ~^:?*[\.
func refChar(c rune) bool {
	return c > ' ' && c < 0x7f && !strings.ContainsRune(`~^:?*[\`, c)
}
