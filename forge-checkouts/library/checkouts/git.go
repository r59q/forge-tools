package checkouts

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

var errGit = errors.New("checkouts: Git operation failed")

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
	return (gitView{directory: directory}).run(ctx, args...)
}

type gitView struct {
	directory string
	env       []string
	isolated  bool
}

func (v gitView) run(ctx context.Context, args ...string) (string, error) {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "maintenance.auto=false", "-c", "gc.auto=0"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...) //nolint:gosec // fixed binary; callers validate user input.
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = v.directory
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}

	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.Env = append(cmd.Env, v.env...)
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

func status(ctx context.Context, view gitView) (Status, error) {
	var s Status
	rev, err := view.run(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return s, err
	}

	s.Revision = strings.TrimSpace(rev)
	branch, err := view.run(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		s.Branch = strings.TrimSpace(branch)
	}

	out, err := view.run(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching", view.submodules())
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

func (v gitView) submodules() string {
	if v.isolated {
		// Nested repositories have their own untrusted configuration too.
		return "--ignore-submodules=all"
	}

	return "--ignore-submodules=none"
}
