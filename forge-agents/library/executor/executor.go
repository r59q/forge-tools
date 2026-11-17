// Package executor runs client CLIs. Local runs them as processes on this
// host; other executors may run them elsewhere behind the same interface.
package executor

import (
	"context"
	"errors"
	"io"
)

// ErrStart reports a process that could not start.
var ErrStart = errors.New("cannot start client")

// Command is one client process.
type Command struct {
	Path string
	Args []string
	// Dir is the working directory: the agent workspace.
	Dir string
	// Env is the whole environment; nothing is inherited.
	Env []string
	// Stdin is written to standard input, which is then closed.
	Stdin []byte
}

// Exit is how a process ended.
type Exit struct {
	// Code is the exit code, or -1 when the process was killed.
	Code int
	// Stderr is the tail of standard error.
	Stderr []byte
}

// Executor runs commands.
type Executor interface {
	// Run starts cmd, passes its standard output to stdout, and waits for
	// it to exit. Cancelling ctx kills the process and everything it
	// started. The error is ErrStart, or ctx's error when it was cancelled.
	Run(ctx context.Context, cmd Command, stdout func(io.Reader)) (Exit, error)
}
