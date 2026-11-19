package folder

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

// stagingPrefix starts the name of the folder an entry is written to before
// it is renamed into place. No valid entry name starts with '.'.
const stagingPrefix = ".add-"

// Add stores a new entry in .evidence/<commit>/<name>, creating the
// .evidence and commit folders when missing, and returns its metadata.
//
// The draft is checked against the limits before anything is written: an
// invalid draft returns evidence.ErrInvalid and one over a limit, including
// Limits.MetadataBytes, evidence.ErrTooLarge. An existing entry returns
// storage.ErrExist and is left as it is.
//
// The files and then the metadata are written to a hidden folder beside the
// entry folder, which is renamed into place last. A failed Add removes it and
// leaves no entry; it may leave empty .evidence and commit folders.
func (s *Store) Add(ctx context.Context, draft storage.Draft) (evidence.Entry, error) {
	if err := ctx.Err(); err != nil {
		return evidence.Entry{}, err
	}
	if err := checkDraftSize(draft.Files, s.limits); err != nil {
		return evidence.Entry{}, err
	}

	entry := draft.Entry()
	metadata, err := evidence.Encode(entry, s.limits)
	if err != nil {
		return evidence.Entry{}, err
	}
	id := entry.ID()

	commitDir, err := s.makeCommitDir(id.Commit)
	if err != nil {
		return evidence.Entry{}, err
	}
	defer commitDir.close()

	if err := absent(commitDir, id.Name); err != nil {
		return evidence.Entry{}, err
	}

	staging, err := stage(ctx, commitDir, draft.Files, metadata)
	if err != nil {
		return evidence.Entry{}, err
	}
	if err := commitDir.root.Rename(staging, id.Name); err != nil {
		_ = commitDir.root.RemoveAll(staging)
		return evidence.Entry{}, pathError(commitDir.join(id.Name), err)
	}

	entry.Files = slices.SortedFunc(slices.Values(entry.Files), func(a, b evidence.File) int {
		return strings.Compare(a.Name, b.Name)
	})
	return entry, nil
}

// checkDraftSize bounds the amount of data Draft.Entry can hash.
func checkDraftSize(files []storage.Content, limits evidence.Limits) error {
	if len(files) > limits.Files {
		return evidence.ErrTooManyFiles
	}
	remaining := limits.EntryBytes
	for index, file := range files {
		size := int64(len(file.Data))
		if size > limits.FileBytes {
			return fmt.Errorf("draft file %d: %w", index, evidence.ErrFileTooLarge)
		}
		if size > remaining {
			return evidence.ErrEntryTooLarge
		}
		remaining -= size
	}
	return nil
}

// makeCommitDir creates the .evidence and commit folders when missing and
// opens the commit folder.
func (s *Store) makeCommitDir(commit string) (dir, error) {
	evidenceDir, err := makeDir(s.root, evidence.Root)
	if err != nil {
		return dir{}, err
	}
	defer evidenceDir.close()

	return makeDir(evidenceDir, commit)
}

// absent returns storage.ErrExist when anything, even a symlink, is named
// name in d.
func absent(d dir, name string) error {
	_, err := d.root.Lstat(name)
	switch {
	case err == nil:
		return fmt.Errorf("%q: %w", d.join(name), storage.ErrExist)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	}
	return pathError(d.join(name), err)
}

// stage writes the files and metadata of an entry to a new hidden folder in
// commitDir and returns its name. On failure the folder is removed.
func stage(ctx context.Context, commitDir dir, files []storage.Content, metadata []byte) (string, error) {
	name := stagingPrefix + rand.Text()

	if err := commitDir.root.Mkdir(name, dirMode); err != nil {
		return "", pathError(commitDir.join(name), err)
	}
	if err := fill(ctx, commitDir, name, files, metadata); err != nil {
		_ = commitDir.root.RemoveAll(name)
		return "", err
	}
	return name, nil
}

// fill writes the files of an entry, then its metadata, to the staging
// folder name. The metadata is last, so a partly written folder has none.
func fill(ctx context.Context, commitDir dir, name string, files []storage.Content, metadata []byte) error {
	stagingDir, err := openDir(commitDir, name)
	if err != nil {
		return err
	}
	defer stagingDir.close()

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeFile(stagingDir, file.Name, file.Data); err != nil {
			return err
		}
	}
	return writeFile(stagingDir, evidence.MetadataFile, metadata)
}
