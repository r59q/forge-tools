package explorer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

// Worktree pins an open root directory, not its changing content. Methods are safe
// concurrently; callers must finish operations before Close. In-root symlinks may
// be read directly; discovery skips them. os.Root confines all symlink resolution.
type Worktree struct {
	root   *os.Root
	source Source
}

// Open opens an operator-configured directory. IDs are public names, not host paths.
func Open(id, label, directory string) (*Worktree, error) {
	if !validID(id) || label == "" || len(label) > 200 {
		return nil, ErrInvalid
	}

	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrForbidden
	}

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		_ = root.Close()
		return nil, err
	}

	return &Worktree{root: root, source: Source{
		ID: id, Label: label, Version: hex.EncodeToString(nonce[:]), Kind: "worktree", Limits: DefaultLimits(),
	}}, nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}

	for _, r := range id {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !valid {
			return false
		}
	}

	return true
}

// Source returns public identity, capability and budget metadata.
func (w *Worktree) Source() Source { return w.source }

// Close releases the directory handle.
func (w *Worktree) Close() error { return w.root.Close() }

func validPath(name string) bool {
	if !fs.ValidPath(name) || len(name) > 4096 || strings.ContainsAny(name, "\\\x00") || !utf8.ValidString(name) {
		return false
	}

	for _, part := range strings.Split(name, "/") {
		if part == ".git" {
			return false
		}
	}

	return true
}

func fileError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}

	return ErrForbidden
}

func (w *Worktree) version(name string, content []byte) string {
	h := sha256.New()
	_, _ = io.WriteString(h, w.source.ID+"\x00"+w.source.Version+"\x00"+name+"\x00")
	_, _ = h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// Read reads bounded regular UTF-8 content. expected is an optional previous file
// version; a mismatch is ErrChanged. Detectable concurrent writes are also rejected.
// The result is an observation, not an immutable filesystem snapshot.
func (w *Worktree) Read(ctx context.Context, name, expected string) (File, error) {
	if !validPath(name) {
		return File{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return File{}, err
	}

	// Nonblocking open prevents a concurrently substituted FIFO from hanging reads.
	f, err := w.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return File{}, fileError(err)
	}
	defer func() { _ = f.Close() }()

	data, err := w.readStable(ctx, f)
	if err != nil {
		return File{}, err
	}

	version := w.version(name, data)
	if expected != "" && expected != version {
		return File{}, ErrChanged
	}

	return File{Location: Location{Source: w.source.ID, Version: version, Path: name}, Text: string(data)}, nil
}

func (w *Worktree) readStable(ctx context.Context, f *os.File) ([]byte, error) {
	before, err := f.Stat()
	if err != nil {
		return nil, fileError(err)
	}
	if !before.Mode().IsRegular() {
		return nil, ErrUnsupported
	}
	if before.Size() > int64(w.source.Limits.FileBytes) {
		return nil, ErrTooLarge
	}

	data, err := io.ReadAll(io.LimitReader(f, int64(w.source.Limits.FileBytes)+1))
	if err != nil {
		return nil, fileError(err)
	}
	if len(data) > w.source.Limits.FileBytes {
		return nil, ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	after, err := f.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || int64(len(data)) != after.Size() {
		return nil, ErrChanged
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return nil, ErrUnsupported
	}

	return data, nil
}

// Browse lists at most 1000 children. Enumeration skips .git and reports symlinks
// and special files as unsupported. A truncated listing is an OS-order prefix.
func (w *Worktree) Browse(ctx context.Context, name string) (Directory, error) {
	if !validPath(name) {
		return Directory{}, ErrInvalid
	}
	entries, truncated, err := w.entries(ctx, name, 1000)
	if err != nil {
		return Directory{}, err
	}

	out := Directory{Source: w.source.ID, Entries: []Entry{}, State: State{Truncated: truncated}}
	for _, entry := range entries {
		if entry.Name() == ".git" || !utf8.ValidString(entry.Name()) {
			out.State.Skipped++
			continue
		}
		kind := "unsupported"
		if entry.IsDir() {
			kind = "directory"
		} else if entry.Type().IsRegular() {
			kind = "file"
		}
		out.Entries = append(out.Entries, Entry{Path: path.Join(name, entry.Name()), Kind: kind})
	}

	var identity strings.Builder
	for _, entry := range out.Entries {
		identity.WriteString(entry.Kind + "\x00" + entry.Path + "\x00")
	}
	out.Version = w.version(name, []byte(identity.String()))
	return out, nil
}

func (w *Worktree) entries(ctx context.Context, name string, limit int) ([]fs.DirEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	f, err := w.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false, fileError(err)
	}
	defer func() { _ = f.Close() }()

	entries, err := f.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, ErrUnsupported
	}
	truncated := len(entries) > limit
	if truncated {
		entries = entries[:limit]
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, truncated, ctx.Err()
}
