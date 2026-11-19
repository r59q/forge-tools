package evidence

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// FormatVersion is the metadata format version Encode writes and Decode
// reads.
const FormatVersion = 1

// metadataV1 is the metadata file of format version 1. Fields are written in
// declaration order.
type metadataV1 struct {
	Version int       `json:"version"`
	Name    string    `json:"name"`
	Title   string    `json:"title"`
	Kind    Kind      `json:"kind"`
	Subject subjectV1 `json:"subject"`
	Files   []fileV1  `json:"files"`
}

type subjectV1 struct {
	Commit string `json:"commit"`
	Path   string `json:"path,omitempty"`
}

type fileV1 struct {
	Name   string `json:"name"`
	Size   *int64 `json:"size"`
	SHA256 string `json:"sha256"`
}

// versionOnly reads the version of any format.
type versionOnly struct {
	Version *int `json:"version"`
}

// Encode returns the metadata file of a valid entry, in format version
// FormatVersion: indented JSON with a final newline. The same entry, with its
// files in any order, always gives the same bytes. An invalid entry returns
// ErrInvalid, and an entry over the limits, or metadata over
// Limits.MetadataBytes, returns ErrTooLarge.
func Encode(entry Entry, limits Limits) ([]byte, error) {
	limits = limits.WithDefaults()

	if err := entry.Validate(limits); err != nil {
		return nil, err
	}

	data, err := json.Marshal(toMetadata(entry), jsontext.Multiline(true), jsontext.WithIndent("  "))
	if err != nil {
		return nil, invalid("metadata", "cannot be encoded")
	}
	data = append(data, '\n')

	if int64(len(data)) > limits.MetadataBytes {
		return nil, fmt.Errorf("metadata: %w", ErrTooLarge)
	}
	return data, nil
}

// Decode reads a metadata file. Input over Limits.MetadataBytes returns
// ErrTooLarge before it is parsed. A version other than FormatVersion returns
// ErrVersion. Malformed JSON, duplicate or unknown fields, a missing required
// field and an invalid entry return ErrInvalid; an entry over the limits
// returns ErrTooLarge. Files are returned in name order.
func Decode(data []byte, limits Limits) (Entry, error) {
	limits = limits.WithDefaults()

	if int64(len(data)) > limits.MetadataBytes {
		return Entry{}, fmt.Errorf("metadata: %w", ErrTooLarge)
	}
	if err := checkVersion(data); err != nil {
		return Entry{}, err
	}

	var metadata metadataV1
	if err := json.Unmarshal(data, &metadata, json.RejectUnknownMembers(true)); err != nil {
		return Entry{}, malformed(err)
	}

	entry, err := fromMetadata(metadata)
	if err != nil {
		return Entry{}, err
	}
	if err := entry.Validate(limits); err != nil {
		return Entry{}, err
	}

	entry.Files = sortedFiles(entry.Files)
	return entry, nil
}

func checkVersion(data []byte) error {
	var version versionOnly
	if err := json.Unmarshal(data, &version); err != nil {
		return malformed(err)
	}

	switch {
	case version.Version == nil:
		return invalid("version", "is missing")
	case *version.Version != FormatVersion:
		return fmt.Errorf("metadata: %w", ErrVersion)
	}
	return nil
}

func toMetadata(entry Entry) metadataV1 {
	files := make([]fileV1, 0, len(entry.Files))
	for _, file := range sortedFiles(entry.Files) {
		files = append(files, fileV1{Name: file.Name, Size: &file.Size, SHA256: file.SHA256})
	}

	return metadataV1{
		Version: FormatVersion,
		Name:    entry.Name,
		Title:   entry.Title,
		Kind:    entry.Kind,
		Subject: subjectV1{Commit: entry.Subject.Commit, Path: entry.Subject.Path},
		Files:   files,
	}
}

func fromMetadata(metadata metadataV1) (Entry, error) {
	files := make([]File, 0, len(metadata.Files))
	for index, file := range metadata.Files {
		if file.Size == nil {
			return Entry{}, fmt.Errorf("file %d: %w", index, invalid("size", "is missing"))
		}
		files = append(files, File{Name: file.Name, Size: *file.Size, SHA256: file.SHA256})
	}

	return Entry{
		Name:    metadata.Name,
		Title:   metadata.Title,
		Kind:    metadata.Kind,
		Subject: Subject{Commit: metadata.Subject.Commit, Path: metadata.Subject.Path},
		Files:   files,
	}, nil
}

func sortedFiles(files []File) []File {
	return slices.SortedFunc(slices.Values(files), func(a, b File) int {
		return strings.Compare(a.Name, b.Name)
	})
}

// malformed returns ErrMalformed with the byte offset of a JSON error. The JSON
// error itself is dropped, since it may quote the input.
func malformed(err error) error {
	var syntactic *jsontext.SyntacticError
	if errors.As(err, &syntactic) {
		return fmt.Errorf("metadata is not valid JSON at byte %d: %w", syntactic.ByteOffset, ErrMalformed)
	}

	var semantic *json.SemanticError
	if errors.As(err, &semantic) {
		return fmt.Errorf("metadata does not match the format at byte %d: %w", semantic.ByteOffset, ErrMalformed)
	}
	return ErrMalformed
}
