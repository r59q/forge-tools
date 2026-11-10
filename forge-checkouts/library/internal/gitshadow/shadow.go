// Package gitshadow builds host-owned Git directories from untrusted Git data.
package gitshadow

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Init writes a minimal, trusted repository with the given object alternates.
// revision determines the object format and initial detached HEAD.
func Init(directory, revision string, alternates ...string) error {
	for _, name := range []string{"objects/info", "refs/heads", "refs/tags", "hooks"} {
		if err := os.MkdirAll(filepath.Join(directory, name), 0700); err != nil {
			return err
		}
	}

	config := "[core]\nrepositoryformatversion = 0\nbare = false\n"
	if len(revision) == 64 {
		config = "[core]\nrepositoryformatversion = 1\nbare = false\n[extensions]\nobjectFormat = sha256\n"
	}
	for name, value := range map[string]string{
		"config":                  config,
		"HEAD":                    revision + "\n",
		"objects/info/alternates": alternateLines(alternates),
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(value), 0600); err != nil {
			return err
		}
	}

	return nil
}

func alternateLines(paths []string) string {
	var lines []string
	for _, path := range paths {
		// Git accepts C-style quoted paths; quotes and newlines need escaping.
		path = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r", "\t", "\\t").Replace(path)
		lines = append(lines, "\""+path+"\"")
	}

	return strings.Join(lines, "\n") + "\n"
}

// Snapshot imports only Git data. It never copies config, hooks, commondir,
// grafts or alternate paths supplied by the borrower. Objects are copied into a
// private alternate so Git cannot follow object symlinks or recursive alternates.
func Snapshot(ctx context.Context, copyPath, directory, cache, revision string) error {
	copyRoot, err := os.OpenRoot(copyPath)
	if err != nil {
		return err
	}
	defer copyRoot.Close()

	source, err := copyRoot.OpenRoot(".git")
	if err != nil {
		return err
	}
	defer source.Close()
	if _, err = source.Lstat("HEAD"); err != nil {
		return err
	}

	borrowed := filepath.Join(directory, "borrowed-objects")
	if err = os.Mkdir(borrowed, 0700); err != nil {
		return err
	}
	if err = Init(directory, revision, borrowed, filepath.Join(cache, "objects")); err != nil {
		return err
	}
	target, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer target.Close()

	return fs.WalkDir(source.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !wanted(path) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		targetPath := path
		if strings.HasPrefix(path, "objects/") {
			targetPath = "borrowed-objects/" + strings.TrimPrefix(path, "objects/")
		}
		if entry.IsDir() {
			return target.MkdirAll(targetPath, 0700)
		}
		return copyData(source, target, path, targetPath)
	})
}

func wanted(path string) bool {
	switch path {
	case ".", "HEAD", "index", "packed-refs", "refs", "objects":
		return true
	case "objects/info":
		return false
	}

	return strings.HasPrefix(path, "refs/") || strings.HasPrefix(path, "objects/") ||
		strings.HasPrefix(path, "sharedindex.") && !strings.Contains(path, "/")
}

func copyData(source, target *os.Root, path, targetPath string) error {
	// O_NONBLOCK avoids waiting on a substituted FIFO; O_NOFOLLOW refuses links.
	in, err := source.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("gitshadow: expected a regular file")
	}

	out, err := target.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}
