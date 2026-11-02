package workspaces

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

const outputLimit = 8 << 20

var errGit = errors.New("workspaces: Git operation failed")

type boundedOutput struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > outputLimit {
		b.exceeded = true
		return 0, ErrTooLarge
	}
	return b.buffer.Write(p)
}

// git disables inherited Git configuration, hooks, prompting and external diffs.
// Existing local repositories and their repository configuration must be trusted.
func git(ctx context.Context, directory string, args ...string) (string, error) {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "maintenance.auto=false", "-c", "gc.auto=0"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = directory
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var out boundedOutput
	cmd.Stdout = &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if out.exceeded {
		return "", ErrTooLarge
	}
	if err != nil {
		return "", errGit
	}
	return out.buffer.String(), nil
}

func status(ctx context.Context, path string) (Status, error) {
	var s Status
	rev, err := git(ctx, path, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return s, err
	}
	s.Revision = strings.TrimSpace(rev)
	branch, err := git(ctx, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		s.Branch = strings.TrimSpace(branch)
	}
	out, err := git(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if err != nil {
		return s, err
	}
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts)-1; i++ {
		p := parts[i]
		if len(p) < 4 {
			return s, ErrConflict
		}
		f := File{Path: p[3:], Index: p[:1], Worktree: p[1:2]}
		if strings.ContainsAny(p[:2], "RC") {
			i++
			if i >= len(parts)-1 {
				return s, ErrConflict
			}
			f.OldPath = parts[i]
		}
		s.Files = append(s.Files, f)
	}
	return s, nil
}
