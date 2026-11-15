// Package checkout turns a checkout directory into code documents for the
// search package: it searches a directory unindexed, and syncs it into an
// index so that later syncs only touch the files that changed.
//
// Checkout content is untrusted. Every read goes through an *os.Root, so
// nothing outside the root is read, and discovery skips symlinks, special
// files and every entry named ".git". Ignored and vendored files are read
// like any other file. Errors name paths relative to the root, never host
// paths or file content.
package checkout

import (
	"errors"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// Error categories. Match them with errors.Is. A tree over a limit returns
// search.ErrTooLarge and a bad request returns search.ErrInvalid.
var (
	// ErrChanged reports a file or directory that changed while it was read.
	// Reading again, or a later sync, sees the new content.
	ErrChanged = errors.New("checkout changed while reading")

	// ErrUnreadable reports a file or directory that cannot be read, such as
	// one without permission.
	ErrUnreadable = errors.New("checkout entry is unreadable")
)

// Limits bound one read or sync of a directory tree. Zero fields use the
// default: DefaultLimits for Read and Search, DefaultSyncLimits for Sync.
type Limits struct {
	// FileBytes is the largest file read. Larger files are skipped and
	// counted in State.TooLarge.
	FileBytes int `json:"fileBytes"`

	// Entries is the most directory entries visited, of any type. More
	// return search.ErrTooLarge.
	Entries int `json:"entries"`

	// Depth is the most directory levels below the root. A deeper directory
	// returns search.ErrTooLarge.
	Depth int `json:"depth"`

	// TotalBytes is the most file content read. Read stops at it and sets
	// State.Truncated; Sync returns search.ErrTooLarge.
	TotalBytes int64 `json:"totalBytes"`
}

// DefaultLimits returns the bounds of one unindexed search, as in
// forge-explorer: 1 MiB per file, 10000 entries, depth 64, 64 MiB in all.
func DefaultLimits() Limits {
	return Limits{FileBytes: 1 << 20, Entries: 10000, Depth: 64, TotalBytes: 64 << 20}
}

// DefaultSyncLimits returns bounds that fit the default index limits of the
// search package: 1 MiB per file, 100000 entries, depth 64, 1 GiB in all.
func DefaultSyncLimits() Limits {
	return Limits{FileBytes: 1 << 20, Entries: 100000, Depth: 64, TotalBytes: 1 << 30}
}

func (l Limits) withDefaults(defaults Limits) Limits {
	if l.FileBytes <= 0 {
		l.FileBytes = defaults.FileBytes
	}
	if l.Entries <= 0 {
		l.Entries = defaults.Entries
	}
	if l.Depth <= 0 {
		l.Depth = defaults.Depth
	}
	if l.TotalBytes <= 0 {
		l.TotalBytes = defaults.TotalBytes
	}
	return l
}

// Options configure a read or sync.
type Options struct {
	// Source is the Ref.Source of every document, such as a checkout ID.
	Source string `json:"source,omitempty"`

	// Limits bound the work. Zero fields use the default.
	Limits Limits `json:"limits"`
}

// State describes the files a read left out.
type State struct {
	// Truncated is set when files were not read because of
	// Limits.TotalBytes.
	Truncated bool `json:"truncated"`

	// TooLarge counts files over Limits.FileBytes.
	TooLarge int `json:"tooLarge"`

	// Unsupported counts entries that are not regular files or directories,
	// such as symlinks, and entries whose path is not a valid document ID.
	Unsupported int `json:"unsupported"`
}

// document returns the search document of a file. Version is the SHA-256 of
// the content, so a sync can tell changed files apart.
func document(source string, file file, content []byte) search.Document {
	return search.Document{
		Ref: search.Ref{
			Kind:    search.KindCode,
			Source:  source,
			ID:      file.path,
			Version: contentVersion(content),
		},
		Content: content,
	}
}

// filter selects the documents a sync of source owns.
func filter(source string) search.Filter {
	return search.Filter{Kind: search.KindCode, Source: source}
}
