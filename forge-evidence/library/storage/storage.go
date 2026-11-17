// Package storage is the boundary between forge-evidence callers and the
// places that keep evidence. A Store is one storage strategy, such as the
// .evidence folder of a repository checkout. Callers name entries by
// evidence.ID and files by name, never by a file system path or object key,
// so a strategy can be swapped without changing them.
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
)

// Error categories every strategy returns, besides evidence.ErrInvalid,
// evidence.ErrVersion and evidence.ErrTooLarge. Match them with errors.Is.
// Messages name entries and files, never file content or host paths.
var (
	// ErrExist reports an entry that already exists. Add never overwrites.
	ErrExist = errors.New("evidence already exists")

	// ErrNotExist reports a missing entry or file.
	ErrNotExist = errors.New("evidence does not exist")

	// ErrUnreadable reports storage that cannot be read or written, such as
	// a folder without permission, or one that changed while it was used.
	ErrUnreadable = errors.New("evidence storage is unreadable")
)

// Store is a storage strategy: it keeps entries, their metadata and their
// files. Implementations are safe for concurrent use.
type Store interface {
	// Add stores a new entry and returns its metadata. An invalid draft
	// returns evidence.ErrInvalid and one over the limits
	// evidence.ErrTooLarge, before anything is stored. An entry with the
	// same ID returns ErrExist and is left as it is. A failed Add stores no
	// entry.
	Add(ctx context.Context, draft Draft) (evidence.Entry, error)

	// Entry returns the metadata of an entry, or ErrNotExist.
	Entry(ctx context.Context, id evidence.ID) (evidence.Entry, error)

	// ReadFile returns the content of one file of an entry, exactly as it
	// was added. A file not listed in the entry's metadata returns
	// ErrNotExist.
	ReadFile(ctx context.Context, id evidence.ID, name string) ([]byte, error)
}

// Draft is an entry to add: its fields and the content of its files. Sizes
// and digests are computed from the content.
type Draft struct {
	// Name identifies the entry among those about the same commit.
	Name string

	// Title is the claim the evidence backs.
	Title string

	// Kind is what the evidence is.
	Kind evidence.Kind

	// Subject is what the evidence is about.
	Subject evidence.Subject

	// Files are the evidence files, in any order.
	Files []Content
}

// Content is one evidence file to add.
type Content struct {
	// Name is the file name, as evidence.ValidName requires.
	Name string

	// Data is the file content. It is stored byte for byte.
	Data []byte
}

// Entry returns the metadata the draft describes, with the size and SHA-256
// of each file. It does not validate the draft.
func (d Draft) Entry() evidence.Entry {
	files := make([]evidence.File, 0, len(d.Files))
	for _, content := range d.Files {
		files = append(files, describe(content))
	}

	return evidence.Entry{
		Name:    d.Name,
		Title:   d.Title,
		Kind:    d.Kind,
		Subject: d.Subject,
		Files:   files,
	}
}

func describe(content Content) evidence.File {
	sum := sha256.Sum256(content.Data)

	return evidence.File{
		Name:   content.Name,
		Size:   int64(len(content.Data)),
		SHA256: hex.EncodeToString(sum[:]),
	}
}
