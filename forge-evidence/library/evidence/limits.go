package evidence

import "errors"

// Stable error categories. Match them with errors.Is; their messages never
// contain metadata or file content.
var (
	// ErrInvalid reports a malformed entry, name, subject or metadata file.
	ErrInvalid = errors.New("invalid evidence")

	// ErrVersion reports metadata in a format version this library does not
	// read.
	ErrVersion = errors.New("unsupported evidence format version")

	// ErrTooLarge reports metadata, a file, an entry or a listing over a size
	// or count limit.
	ErrTooLarge = errors.New("evidence exceeds limit")
)

// Limits bounds the evidence the library accepts. Zero or negative fields use
// the default.
type Limits struct {
	// MetadataBytes is the largest metadata file.
	MetadataBytes int64

	// FileBytes is the largest evidence file.
	FileBytes int64

	// Files is the most evidence files in one entry.
	Files int

	// EntryBytes is the most bytes of evidence files in one entry.
	EntryBytes int64

	// Entries bounds the directory entries scanned by a listing. The folder
	// strategy applies it separately to the evidence root and to the total
	// across all commit folders.
	Entries int
}

// DefaultLimits returns the default limits. They are small because evidence
// in a repository stays in its Git history for good.
func DefaultLimits() Limits {
	return Limits{
		MetadataBytes: 64 << 10,
		FileBytes:     4 << 20,
		Files:         32,
		EntryBytes:    16 << 20,
		Entries:       10000,
	}
}

// WithDefaults returns the limits with zero or negative fields set to their
// default.
func (l Limits) WithDefaults() Limits {
	defaults := DefaultLimits()

	if l.MetadataBytes <= 0 {
		l.MetadataBytes = defaults.MetadataBytes
	}
	if l.FileBytes <= 0 {
		l.FileBytes = defaults.FileBytes
	}
	if l.Files <= 0 {
		l.Files = defaults.Files
	}
	if l.EntryBytes <= 0 {
		l.EntryBytes = defaults.EntryBytes
	}
	if l.Entries <= 0 {
		l.Entries = defaults.Entries
	}
	return l
}
