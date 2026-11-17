package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

const (
	defaultStderr = 64 << 10
	waitDelay     = 5 * time.Second
)

// Local runs commands as processes on this host, each in its own process
// group so that cancelling kills what the client started too.
type Local struct {
	// StderrLimit bounds the kept tail of standard error; 0 means 64 KiB.
	StderrLimit int
}

// Run starts cmd and waits for it.
func (l Local) Run(ctx context.Context, cmd Command, stdout func(io.Reader)) (Exit, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...) //nolint:gosec // operator-configured binary; adapters build the arguments.
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	c.Stdin = bytes.NewReader(cmd.Stdin)
	limit := l.StderrLimit
	if limit <= 0 {
		limit = defaultStderr
	}
	tail := &tailBuffer{limit: limit}
	c.Stderr = tail
	group(c)
	c.WaitDelay = waitDelay
	out, err := c.StdoutPipe()
	if err != nil {
		return Exit{}, fmt.Errorf("%w: %w", ErrStart, err)
	}
	if err := c.Start(); err != nil {
		return Exit{}, fmt.Errorf("%w: %w", ErrStart, err)
	}
	stdout(out)
	// Drain what the caller did not read, so the process is not blocked.
	_, _ = io.Copy(io.Discard, out)
	err = c.Wait()
	exit := Exit{Code: c.ProcessState.ExitCode(), Stderr: tail.Bytes()}
	if ctx.Err() != nil {
		return exit, ctx.Err()
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return exit, fmt.Errorf("client process: %w", err)
	}
	return exit, nil
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return bytes.Clone(t.buf)
}
