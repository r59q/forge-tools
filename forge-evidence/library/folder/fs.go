package folder

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

// Modes of created folders and files. Evidence is committed and shared, so
// it is not private.
const (
	dirMode  = 0o755
	fileMode = 0o644
)

// dir is a folder opened without following symlinks. Path is relative to the
// repository root, for errors.
type dir struct {
	root *os.Root
	path string
}

func (d dir) close() {
	_ = d.root.Close()
}

func (d dir) join(name string) string {
	if d.path == "" {
		return name
	}
	return d.path + "/" + name
}

// openDir opens the folder name in parent. A symlink or any other entry that
// is not a folder returns evidence.ErrInvalid, so no symlink is followed,
// even one that stays inside the parent.
func openDir(parent dir, name string) (dir, error) {
	path := parent.join(name)

	before, err := parent.root.Lstat(name)
	if err != nil {
		return dir{}, pathError(path, err)
	}
	if !before.IsDir() {
		return dir{}, fmt.Errorf("%q is not a folder: %w", path, evidence.ErrInvalid)
	}

	root, err := parent.root.OpenRoot(name)
	if err != nil {
		return dir{}, pathError(path, err)
	}
	opened := dir{root: root, path: path}

	// The folder may have been replaced, such as by a symlink, between Lstat
	// and OpenRoot. The opened folder must be the one Lstat saw.
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		opened.close()
		return dir{}, fmt.Errorf("%q: %w", path, storage.ErrUnreadable)
	}
	return opened, nil
}

// makeDir creates the folder name in parent if it is missing and opens it.
func makeDir(parent dir, name string) (dir, error) {
	err := parent.root.Mkdir(name, dirMode)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return dir{}, pathError(parent.join(name), err)
	}
	return openDir(parent, name)
}

// readFile reads the regular file name in d. A file over limit bytes returns
// evidence.ErrTooLarge before its content is read. Symlinks and special
// files return evidence.ErrInvalid and are not opened.
func readFile(d dir, name string, limit int64) ([]byte, error) {
	path := d.join(name)

	before, err := d.root.Lstat(name)
	if err != nil {
		return nil, pathError(path, err)
	}
	if err := checkRegular(path, before, limit); err != nil {
		return nil, err
	}

	// O_NONBLOCK keeps a file replaced by a FIFO from hanging the read.
	handle, err := d.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, pathError(path, err)
	}
	defer func() { _ = handle.Close() }()

	opened, err := handle.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("%q: %w", path, storage.ErrUnreadable)
	}
	if err := checkRegular(path, opened, limit); err != nil {
		return nil, err
	}
	return readAll(path, handle, opened.Size())
}

func checkRegular(path string, info fs.FileInfo, limit int64) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%q is not a regular file: %w", path, evidence.ErrInvalid)
	}
	if info.Size() > limit {
		return fmt.Errorf("%q: %w", path, evidence.ErrTooLarge)
	}
	return nil
}

// readAll reads a file of a known size. A file that grows or shrinks while it
// is read returns storage.ErrUnreadable, so content is never truncated.
func readAll(path string, handle *os.File, size int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(handle, size+1))
	if err != nil {
		return nil, fmt.Errorf("%q: %w", path, storage.ErrUnreadable)
	}
	if int64(len(content)) != size {
		return nil, fmt.Errorf("%q changed while reading: %w", path, storage.ErrUnreadable)
	}
	return content, nil
}

// writeFile creates the file name in d with the given content. It never
// opens an existing file or follows a symlink.
func writeFile(d dir, name string, content []byte) error {
	path := d.join(name)

	handle, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return pathError(path, err)
	}

	_, err = handle.Write(content)
	if err == nil {
		err = handle.Sync()
	}
	if closeErr := handle.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("%q: %w", path, storage.ErrUnreadable)
	}
	return nil
}

// pathError maps a file system error to a category. The original error is
// dropped: it may hold a host path.
func pathError(path string, err error) error {
	return fmt.Errorf("%q: %w", path, category(err))
}

// category returns the storage error category of a file system error.
func category(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return storage.ErrNotExist
	case errors.Is(err, fs.ErrExist):
		return storage.ErrExist
	}
	return storage.ErrUnreadable
}
