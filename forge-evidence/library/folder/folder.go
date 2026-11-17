// Package folder is the repository-folder storage strategy: it keeps
// evidence as ordinary files in the .evidence folder of a repository
// checkout, in the layout and metadata format of package evidence.
//
// The checkout is untrusted. Every read and write stays inside .evidence
// below the root: no symlink is followed, including a symlinked .evidence,
// and special files are never read. Sizes are checked before content is read.
// Errors name paths relative to the root, never host paths or file content.
//
// The package does not run Git: it does not commit, push or check that a
// subject commit exists.
package folder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"slices"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

// Store is the .evidence folder of one repository checkout. It is safe for
// concurrent use.
type Store struct {
	root   dir
	limits evidence.Limits
}

var _ storage.Store = (*Store)(nil)

// Open opens the evidence of the checkout at path, the repository root, which
// must be a folder. The .evidence folder need not exist yet. Zero or negative
// limits use the default. Close the store when done.
func Open(path string, limits evidence.Limits) (*Store, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("repository root: %w", category(err))
	}
	return &Store{root: dir{root: root}, limits: limits.WithDefaults()}, nil
}

// Close releases the repository root.
func (s *Store) Close() error {
	return s.root.root.Close()
}

// Entry returns the metadata of an entry, read from its MetadataFile. A
// missing entry returns storage.ErrNotExist, metadata over
// Limits.MetadataBytes evidence.ErrTooLarge, and metadata that does not
// decode, or that names another entry, evidence.ErrInvalid.
func (s *Store) Entry(ctx context.Context, id evidence.ID) (evidence.Entry, error) {
	if err := ctx.Err(); err != nil {
		return evidence.Entry{}, err
	}

	entryDir, err := s.openEntry(id)
	if err != nil {
		return evidence.Entry{}, err
	}
	defer entryDir.close()

	return loadEntry(entryDir, id, s.limits)
}

// ReadFile returns the content of a file listed in an entry's metadata. A
// file over Limits.FileBytes returns evidence.ErrTooLarge before it is read;
// one whose size or SHA-256 differs from its metadata returns
// evidence.ErrInvalid.
func (s *Store) ReadFile(ctx context.Context, id evidence.ID, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := evidence.ValidName(name); err != nil {
		return nil, fmt.Errorf("file %w", err)
	}

	entryDir, err := s.openEntry(id)
	if err != nil {
		return nil, err
	}
	defer entryDir.close()

	entry, err := loadEntry(entryDir, id, s.limits)
	if err != nil {
		return nil, err
	}

	path := entryDir.join(name)
	file, listed := findFile(entry.Files, name)
	if !listed {
		return nil, fmt.Errorf("%q is not listed in the metadata: %w", path, storage.ErrNotExist)
	}

	content, err := readFile(entryDir, name, s.limits.FileBytes)
	if err != nil {
		return nil, err
	}
	if !matches(file, content) {
		return nil, fmt.Errorf("%q does not match its metadata: %w", path, evidence.ErrInvalid)
	}
	return content, nil
}

// openEntry opens the folder of an entry, without following symlinks.
func (s *Store) openEntry(id evidence.ID) (dir, error) {
	if err := id.Validate(); err != nil {
		return dir{}, err
	}

	evidenceDir, err := openDir(s.root, evidence.Root)
	if err != nil {
		return dir{}, err
	}
	defer evidenceDir.close()

	commitDir, err := openDir(evidenceDir, id.Commit)
	if err != nil {
		return dir{}, err
	}
	defer commitDir.close()

	return openDir(commitDir, id.Name)
}

// loadEntry reads and decodes the metadata in an entry folder, and checks
// that it describes the entry the folder is for.
func loadEntry(entryDir dir, id evidence.ID, limits evidence.Limits) (evidence.Entry, error) {
	path := entryDir.join(evidence.MetadataFile)

	data, err := readFile(entryDir, evidence.MetadataFile, limits.MetadataBytes)
	if err != nil {
		return evidence.Entry{}, err
	}

	entry, err := evidence.Decode(data, limits)
	if err != nil {
		return evidence.Entry{}, fmt.Errorf("%q: %w", path, err)
	}
	if entry.ID() != id {
		return evidence.Entry{}, fmt.Errorf("%q names another entry: %w", path, evidence.ErrInvalid)
	}
	return entry, nil
}

func findFile(files []evidence.File, name string) (evidence.File, bool) {
	index := slices.IndexFunc(files, func(file evidence.File) bool { return file.Name == name })
	if index < 0 {
		return evidence.File{}, false
	}
	return files[index], true
}

// matches reports whether content has the size and SHA-256 of its metadata.
func matches(file evidence.File, content []byte) bool {
	sum := sha256.Sum256(content)
	return int64(len(content)) == file.Size && hex.EncodeToString(sum[:]) == file.SHA256
}
