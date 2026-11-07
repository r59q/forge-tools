// Package storage keeps reactor state and managed webhook sources durably in a local directory on Unix
// systems.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	"github.com/r59q/forge-tools/forge-reactor/library/sources"
)

// ErrLocked means another process holds the data directory.
var ErrLocked = errors.New("storage: data directory is in use")

// Files inside the directory.
const (
	// stateFile holds the reactor state.
	stateFile = "reactor.json"
	// sourcesFile holds managed webhook sources and their secrets.
	sourcesFile = "sources.json"
)

// File is one atomic JSON document in a private directory. One process owns
// the directory at a time, holding an advisory flock until Close. Use a local
// filesystem supporting flock, atomic rename and fsync; not network storage.
// Every update rewrites the document, so this store suits modest volumes.
type File struct {
	directory string
	lock      *os.File
	store     *reactor.JSONStore
	sources   *sources.MemoryStore
}

type envelope struct {
	Version int             `json:"version"`
	State   json.RawMessage `json:"state"`
}

// Open creates the directory if needed, takes ownership of it and loads the
// existing state. The caller must prevent untrusted filesystem access.
func Open(directory string) (*File, error) {
	if directory == "" {
		return nil, reactor.ErrInvalid
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}

	path := filepath.Join(directory, "store.lock")
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the operator names the data directory.
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}

		return nil, err
	}

	f := &File{directory: directory, lock: lock}
	if err := f.open(); err != nil {
		_ = f.Close()

		return nil, err
	}

	return f, nil
}

// open loads both documents.
func (f *File) open() error {
	data, err := f.load(stateFile)
	if err != nil {
		return err
	}
	f.store, err = reactor.NewJSONStore(data, f.saver(stateFile))
	if err != nil {
		return err
	}

	data, err = f.load(sourcesFile)
	if err != nil {
		return err
	}
	f.sources, err = sources.NewMemoryStore(data, f.saver(sourcesFile))

	return err
}

// Store returns the reactor store backed by the directory.
func (f *File) Store() *reactor.JSONStore { return f.store }

// Sources returns the managed webhook source store backed by the directory.
func (f *File) Sources() *sources.MemoryStore { return f.sources }

// Close releases the directory.
func (f *File) Close() error {
	_ = syscall.Flock(int(f.lock.Fd()), syscall.LOCK_UN)

	return f.lock.Close()
}

// load reads a stored encoding, or nil when there is none.
func (f *File) load(name string) (json.RawMessage, error) {
	b, err := os.ReadFile(filepath.Join(f.directory, name)) //nolint:gosec // a fixed name in the operator's data directory.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var e envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("storage: invalid state file: %w", err)
	}
	if e.Version != 1 || len(e.State) == 0 {
		return nil, errors.New("storage: unsupported state file version")
	}

	return e.State, nil
}

// saver returns a save function for the named document.
func (f *File) saver(name string) func(context.Context, []byte) error {
	return func(ctx context.Context, data []byte) error { return f.save(ctx, name, data) }
}

// save replaces a document using fsync and rename. A directory-sync error
// after rename is ambiguous, so the reactor treats it as a failed commit and
// may repeat work, never lose it.
func (f *File) save(ctx context.Context, name string, data []byte) error {
	b, err := json.Marshal(envelope{Version: 1, State: data})
	if err != nil {
		return err
	}

	temp, err := os.CreateTemp(f.directory, ".reactor-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err := temp.Write(b); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), filepath.Join(f.directory, name)); err != nil {
		return err
	}

	dir, err := os.Open(f.directory)
	if err != nil {
		return err
	}
	defer dir.Close()

	return dir.Sync()
}
