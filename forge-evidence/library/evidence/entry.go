// Package evidence holds the forge-evidence model: entries of evidence files
// with machine-readable metadata, the versioned metadata format and the
// layout of the .evidence folder.
//
// The package is pure: it reads and writes no files. Names, metadata and
// subject paths are untrusted, and errors never contain them.
package evidence

import (
	"fmt"
	"strings"
)

// Kind is what an entry's evidence is. Values are lower-case words; other
// valid kinds than the known ones are accepted as they are.
type Kind string

// Known kinds.
const (
	// KindTestResult is the outcome of tests, such as a JUnit report.
	KindTestResult Kind = "test-result"

	// KindScreenshot is an image of a user interface.
	KindScreenshot Kind = "screenshot"

	// KindLog is output of a program or a run.
	KindLog Kind = "log"

	// KindDocument is a written analysis or report.
	KindDocument Kind = "document"
)

// Entry is one piece of evidence: one or more files that back a claim about a
// subject.
type Entry struct {
	// Name identifies the entry among those about the same commit. It follows
	// ValidName.
	Name string

	// Title is the claim the evidence backs, such as "unit tests pass": one
	// line of 1 to MaxTitleBytes bytes.
	Title string

	// Kind is what the evidence is.
	Kind Kind

	// Subject is what the evidence is about.
	Subject Subject

	// Files are the evidence files, in any order. Encode writes them in name
	// order and Decode returns them in name order.
	Files []File
}

// Subject is what an entry is about.
type Subject struct {
	// Commit is the full SHA of the commit the evidence is about. Required.
	Commit string

	// Path is the repository path the evidence is about, with '/' separators.
	// Empty means the whole commit.
	Path string
}

// File is one evidence file of an entry, as recorded in its metadata.
type File struct {
	// Name is the file name in the entry folder. It follows ValidName and is
	// not MetadataFile.
	Name string

	// Size is the length of the file in bytes.
	Size int64

	// SHA256 is the SHA-256 of the file content, in lowercase hex.
	SHA256 string
}

// ID returns the identity of the entry.
func (e Entry) ID() ID {
	return ID{Commit: e.Subject.Commit, Name: e.Name}
}

// Validate reports whether the entry is valid within the limits. A malformed
// field returns ErrInvalid; more than Limits.Files files, or files over
// Limits.FileBytes or Limits.EntryBytes by their recorded size, return
// ErrTooLarge.
func (e Entry) Validate(limits Limits) error {
	limits = limits.WithDefaults()

	if err := ValidName(e.Name); err != nil {
		return fmt.Errorf("entry %w", err)
	}
	if err := validTitle(e.Title); err != nil {
		return err
	}
	if err := ValidKind(e.Kind); err != nil {
		return err
	}
	if err := e.Subject.Validate(); err != nil {
		return err
	}
	return validFiles(e.Files, limits)
}

// Validate reports whether the subject is valid: a valid commit and an empty
// or valid path. Otherwise it returns ErrInvalid.
func (s Subject) Validate() error {
	if err := ValidCommit(s.Commit); err != nil {
		return fmt.Errorf("subject %w", err)
	}
	if s.Path == "" {
		return nil
	}
	if err := ValidPath(s.Path); err != nil {
		return fmt.Errorf("subject %w", err)
	}
	return nil
}

func validFiles(files []File, limits Limits) error {
	if len(files) == 0 {
		return invalid("files", "are empty")
	}
	if len(files) > limits.Files {
		return fmt.Errorf("files: %w", ErrTooLarge)
	}

	seen := make(map[string]bool, len(files))
	var total int64

	for index, file := range files {
		if err := validFile(file, limits); err != nil {
			return fmt.Errorf("file %d: %w", index, err)
		}

		// Names that differ only in case would collide on some file systems.
		folded := strings.ToLower(file.Name)
		if seen[folded] {
			return fmt.Errorf("file %d: %w", index, invalid("name", "is a duplicate"))
		}
		seen[folded] = true

		if file.Size > limits.EntryBytes-total {
			return fmt.Errorf("files: %w", ErrTooLarge)
		}
		total += file.Size
	}
	return nil
}

func validFile(file File, limits Limits) error {
	if err := ValidName(file.Name); err != nil {
		return err
	}
	if strings.EqualFold(file.Name, MetadataFile) {
		return invalid("name", "is the metadata file name")
	}
	if file.Size < 0 {
		return invalid("size", "is negative")
	}
	if file.Size > limits.FileBytes {
		return fmt.Errorf("size: %w", ErrTooLarge)
	}
	return validDigest(file.SHA256)
}
