package checkouts

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// initCache matches a new bare cache to the remote's advertised object format.
// Existing caches keep their objects and worktree registrations.
func initCache(ctx context.Context, directory, remoteURL string) error {
	if _, err := os.Stat(filepath.Join(directory, "HEAD")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	refs, err := remoteGit(ctx, "", "ls-remote", "--", remoteURL)
	if err != nil {
		return err
	}
	if refs == "" {
		return ErrNotFound
	}

	objectID, _, _ := strings.Cut(refs, "\t")
	decoded, err := hex.DecodeString(objectID)
	if err != nil {
		return errGit
	}

	var format string
	switch len(decoded) {
	case 20:
		format = "sha1"
	case 32:
		format = "sha256"
	default:
		return errGit
	}

	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}

	_, err = git(ctx, "", "init", "--bare", "--object-format="+format, "--", directory)

	return err
}
