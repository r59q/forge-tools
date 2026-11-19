package storage

// Report is the result of checking stored evidence. An empty report means
// the evidence is valid.
type Report struct {
	// Problems lists the detected problems in deterministic order.
	Problems []Problem

	// Truncated is true when additional problems exceeded the report limit.
	Truncated bool
}

// Problem describes a violation without including file or metadata content.
type Problem struct {
	// Path locates the problem, relative to the repository root for a folder.
	Path string

	// Reason is the stable machine-readable category.
	Reason Reason

	// Message briefly explains what is wrong.
	Message string
}

// Reason identifies a kind of validation problem.
type Reason string

const (
	// MissingMetadata means an entry folder has no metadata file.
	MissingMetadata Reason = "missing_metadata"
	// MalformedMetadata means the metadata does not conform to its JSON format.
	MalformedMetadata Reason = "malformed_metadata"
	// OversizedMetadata means the metadata exceeds the byte limit.
	OversizedMetadata Reason = "oversized_metadata"
	// UnknownVersion means the metadata specifies an unsupported version.
	UnknownVersion Reason = "unknown_version"
	// InvalidField means a metadata field is missing, invalid or conflicts with the entry path.
	InvalidField Reason = "invalid_field"
	// UnsafeName means a commit, entry or evidence file has an invalid name.
	UnsafeName Reason = "unsafe_name"
	// Symlink means a path is a symbolic link.
	Symlink Reason = "symlink"
	// SpecialFile means a path is neither a regular file, a directory nor a symlink.
	SpecialFile Reason = "special_file"
	// StrayPath means the layout does not allow a file or directory at the path.
	StrayPath Reason = "stray_path"
	// FileTooLarge means an evidence file exceeds the per-file byte limit.
	FileTooLarge Reason = "file_too_large"
	// TooManyFiles means an entry exceeds the file count limit.
	TooManyFiles Reason = "too_many_files"
	// EntryTooLarge means an entry exceeds the total evidence file byte limit.
	EntryTooLarge Reason = "entry_too_large"
	// MissingFile means a listed file has no corresponding path in the entry.
	MissingFile Reason = "missing_file"
	// UnlistedFile means the metadata does not list an evidence file.
	UnlistedFile Reason = "unlisted_file"
	// FileMismatch means a file's size or SHA-256 differs from its metadata.
	FileMismatch Reason = "file_mismatch"
)
