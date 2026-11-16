package api

import (
	"errors"
	"io/fs"
	"os"
	"sort"
)

// Journal holds the IDs of the checkouts this service created and has not
// yet removed, so that a restarted service removes those a crash left.
type Journal interface {
	// Add records a checkout ID durably.
	Add(id string) error

	// Remove forgets a checkout ID. A missing ID is not an error.
	Remove(id string) error

	// List returns every recorded ID.
	List() ([]string, error)
}

// DirJournal keeps each checkout ID as an empty file in a private
// directory.
type DirJournal struct {
	dir string
}

// OpenJournal creates dir, private to the user if it is new, and returns its
// journal.
func OpenJournal(dir string) (*DirJournal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &DirJournal{dir: dir}, nil
}

// Add creates the file of id and syncs the directory.
func (j *DirJournal) Add(id string) error {
	if !validCheckoutID(id) {
		return errBadRequest
	}
	return j.change(func(root *os.Root) error {
		f, err := root.OpenFile(id, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		return f.Close()
	})
}

// Remove deletes the file of id and syncs the directory.
func (j *DirJournal) Remove(id string) error {
	if !validCheckoutID(id) {
		return errBadRequest
	}
	return j.change(func(root *os.Root) error {
		if err := root.Remove(id); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
}

// List returns the IDs of the directory's files, ignoring other names.
func (j *DirJournal) List() ([]string, error) {
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		return nil, err
	}

	var ids []string
	for _, e := range entries {
		if e.Type().IsRegular() && validCheckoutID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// change runs fn in the directory, then syncs it so the change is durable.
func (j *DirJournal) change(fn func(*os.Root) error) error {
	root, err := os.OpenRoot(j.dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	if err := fn(root); err != nil {
		return err
	}

	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
