// Package storage provides durable checkout storage on Unix systems.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/r59q/forge-tools/forge-checkouts/library/checkouts"
)

// File stores one atomic JSON database in a private directory. Advisory flock
// serializes readers/writers across File instances and processes. Use a local
// filesystem supporting flock, atomic rename and fsync; not network storage.
// Every update rewrites the database, so this store suits modest installations.
type File struct{ directory string }
type envelope struct {
	Version int             `json:"version"`
	State   checkouts.State `json:"state"`
}

// Open creates the directory if needed and checks that existing data is valid.
// It also reads metadata from before the forge-checkouts rename. The first
// successful Update writes checkouts.json and leaves the original file intact.
// The caller owns the directory and must prevent untrusted filesystem access.
func Open(ctx context.Context, directory string) (*File, error) {
	if directory == "" {
		return nil, checkouts.ErrInvalid
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}

	f := &File{directory: directory}
	if _, err := f.Read(ctx); err != nil {
		return nil, err
	}

	return f, nil
}

// jscpd:ignore-start - file locking, shared only with forge-review.
func (f *File) lock(ctx context.Context) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(f.directory, "store.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}

	for {
		if err = ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}

		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, err
		}

		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func unlock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

// jscpd:ignore-end

func (f *File) load() (checkouts.State, error) {
	b, err := os.ReadFile(filepath.Join(f.directory, "checkouts.json"))
	if errors.Is(err, os.ErrNotExist) {
		return f.loadLegacy()
	}
	if err != nil {
		return checkouts.State{}, err
	}

	var e envelope
	if err = json.Unmarshal(b, &e); err != nil {
		return checkouts.State{}, fmt.Errorf("storage: invalid database: %w", err)
	}
	if e.Version != 1 || e.State.Checkouts == nil {
		return checkouts.State{}, errors.New("storage: invalid database version or state")
	}

	return e.State, nil
}

// Read returns an isolated state read under a process-shared lock.
func (f *File) Read(ctx context.Context) (checkouts.State, error) {
	lock, err := f.lock(ctx)
	if err != nil {
		return checkouts.State{}, err
	}

	defer unlock(lock)

	return f.load()
}

// Update atomically commits successful callbacks using fsync and rename. A
// directory-sync error after rename is ambiguous; reread before retrying.
func (f *File) Update(ctx context.Context, fn func(*checkouts.State) error) error {
	// jscpd:ignore-start - atomic file update, shared only with forge-review.
	lock, err := f.lock(ctx)
	if err != nil {
		return err
	}

	defer unlock(lock)
	state, err := f.load()
	if err != nil {
		return err
	}
	if err = fn(&state); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}

	b, err := json.Marshal(envelope{Version: 1, State: state})
	if err != nil {
		return err
	}

	temp, err := os.CreateTemp(f.directory, ".checkouts-*")
	if err != nil {
		return err
	}

	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err = temp.Write(b); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(temp.Name(), filepath.Join(f.directory, "checkouts.json")); err != nil {
		return err
	}
	// jscpd:ignore-end

	dir, err := os.Open(f.directory)
	if err != nil {
		return err
	}

	defer dir.Close()

	return dir.Sync()
}
