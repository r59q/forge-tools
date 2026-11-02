package gitobj

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Mode is a tree entry mode.
type Mode uint32

// Tree entry modes Git writes.
const (
	ModeTree      Mode = 0o040000
	ModeFile      Mode = 0o100644
	ModeExec      Mode = 0o100755
	ModeSymlink   Mode = 0o120000
	ModeSubmodule Mode = 0o160000
)

// String returns the mode in octal as trees store it, such as "100644" or
// "40000", with no leading zero.
func (m Mode) String() string { return strconv.FormatUint(uint64(m), 8) }

// Valid reports whether m is one of the five modes Git writes.
func (m Mode) Valid() bool {
	switch m {
	case ModeTree, ModeFile, ModeExec, ModeSymlink, ModeSubmodule:
		return true
	}
	return false
}

// Type returns the type of object an entry with mode m points to: a tree,
// a commit for a submodule, otherwise a blob.
func (m Mode) Type() Type {
	switch m {
	case ModeTree:
		return TypeTree
	case ModeSubmodule:
		return TypeCommit
	}
	return TypeBlob
}

// TreeEntry is one entry of a tree.
type TreeEntry struct {
	Name string
	Mode Mode
	ID   ID
}

// CheckName returns an error wrapping ErrInvalid if name is not safe as a
// tree entry name. It rejects empty names, ".", "..", names with "/" or
// NUL, and the ".git" spellings that git fsck rejects, in any case.
func CheckName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: empty tree entry name", ErrInvalid)
	case name == "." || name == "..":
		return fmt.Errorf("%w: tree entry name %s", ErrInvalid, quote(name))
	case strings.ContainsAny(name, "/\x00"):
		return fmt.Errorf("%w: tree entry name %s has a slash or NUL", ErrInvalid, quote(name))
	case isDotGit(name):
		return fmt.Errorf("%w: tree entry name %s is reserved", ErrInvalid, quote(name))
	}
	return nil
}

// isDotGit reports whether name is ".git" as git fsck sees it on HFS+ and
// NTFS: any case, ignorable Unicode, trailing spaces or dots, "git~1",
// alternate data streams and backslash separators.
func isDotGit(name string) bool {
	var b strings.Builder
	for _, r := range name {
		if !hfsIgnorable(r) {
			b.WriteRune(r)
		}
	}
	if strings.EqualFold(b.String(), ".git") {
		return true
	}
	for part := range strings.SplitSeq(name, `\`) {
		base, _, ads := strings.Cut(part, ":")
		base = strings.TrimRight(base, " .")
		if strings.EqualFold(base, ".git") || strings.EqualFold(base, "git~1") {
			return true
		}
		if ads {
			return false
		}
	}
	return false
}

// hfsIgnorable reports whether HFS+ ignores r when comparing names.
func hfsIgnorable(r rune) bool {
	return (r >= 0x200c && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) ||
		(r >= 0x206a && r <= 0x206f) || r == 0xfeff
}

// compareEntries orders entries as Git does: by name bytes, with a tree's
// name compared as if it ended in "/".
func compareEntries(a, b TreeEntry) int {
	return strings.Compare(sortKey(a), sortKey(b))
}

func sortKey(e TreeEntry) string {
	if e.Mode == ModeTree {
		return e.Name + "/"
	}
	return e.Name
}

// SortEntries sorts entries in Git's canonical tree order.
func SortEntries(entries []TreeEntry) {
	slices.SortFunc(entries, compareEntries)
}

// EncodeTree returns the content of a tree with entries, sorted in canonical
// order. It does not change entries. It rejects unsafe names (see
// CheckName), modes other than the five Git writes, duplicate names and a
// symlinked ".gitmodules", as git fsck does. An empty list gives the empty
// tree.
func EncodeTree(entries []TreeEntry) ([]byte, error) {
	sorted := slices.Clone(entries)
	SortEntries(sorted)
	seen := make(map[string]bool, len(sorted))
	var b bytes.Buffer
	for _, e := range sorted {
		if err := CheckName(e.Name); err != nil {
			return nil, err
		}
		if !e.Mode.Valid() {
			return nil, fmt.Errorf("%w: entry %s has mode %o", ErrInvalid, quote(e.Name), uint32(e.Mode))
		}
		if seen[e.Name] {
			return nil, fmt.Errorf("%w: duplicate tree entry %s", ErrInvalid, quote(e.Name))
		}
		seen[e.Name] = true
		if e.Mode == ModeSymlink && strings.EqualFold(e.Name, ".gitmodules") {
			return nil, fmt.Errorf("%w: .gitmodules is a symlink", ErrInvalid)
		}
		b.WriteString(e.Mode.String())
		b.WriteByte(' ')
		b.WriteString(e.Name)
		b.WriteByte(0)
		b.Write(e.ID[:])
	}
	return b.Bytes(), nil
}

// DecodeTree parses tree content. It checks the structure only: octal
// modes, non-empty names and whole IDs. It keeps the stored order and does
// not apply CheckName, so it can read any tree a forge holds.
func DecodeTree(data []byte) ([]TreeEntry, error) {
	var out []TreeEntry
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		if sp < 1 || sp > 7 {
			return nil, fmt.Errorf("%w: bad tree entry mode", ErrInvalid)
		}
		var mode uint32
		for _, c := range data[:sp] {
			if c < '0' || c > '7' {
				return nil, fmt.Errorf("%w: bad tree entry mode", ErrInvalid)
			}
			mode = mode<<3 | uint32(c-'0')
		}
		data = data[sp+1:]
		nul := bytes.IndexByte(data, 0)
		if nul < 1 {
			return nil, fmt.Errorf("%w: bad tree entry name", ErrInvalid)
		}
		name := string(data[:nul])
		data = data[nul+1:]
		if len(data) < Size {
			return nil, fmt.Errorf("%w: truncated tree entry %s", ErrInvalid, quote(name))
		}
		e := TreeEntry{Name: name, Mode: Mode(mode)}
		copy(e.ID[:], data)
		data = data[Size:]
		out = append(out, e)
	}
	return out, nil
}
