package executor_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/executor"
)

func TestLocalRun(t *testing.T) {
	var out []byte
	exit, err := executor.Local{StderrLimit: 4}.Run(t.Context(), executor.Command{
		Path:  "/bin/sh",
		Args:  []string{"-c", `read line; echo "$line:$X:$(pwd)"; echo 123456 >&2; exit 3`},
		Dir:   os.TempDir(),
		Env:   []string{"X=y"},
		Stdin: []byte("hello\n"),
	}, func(r io.Reader) { out, _ = io.ReadAll(r) })
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "hello:y:"+os.TempDir() {
		t.Errorf("stdout = %q", got)
	}
	if exit.Code != 3 || string(exit.Stderr) != "456\n" {
		t.Errorf("exit = %d %q", exit.Code, exit.Stderr)
	}
}

func TestLocalCancelKillsGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	start := time.Now()
	exit, err := executor.Local{}.Run(ctx, executor.Command{
		Path: "/bin/sh",
		// The child sleep keeps stdout open unless the group is killed.
		Args: []string{"-c", "sleep 30 & echo started; wait"},
	}, func(r io.Reader) {
		buf := make([]byte, 8)
		_, _ = r.Read(buf)
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if exit.Code != -1 || time.Since(start) > 4*time.Second {
		t.Errorf("exit = %d after %s", exit.Code, time.Since(start))
	}
}

func TestLocalMissingBinary(t *testing.T) {
	_, err := executor.Local{}.Run(t.Context(), executor.Command{Path: "/nonexistent/claude"}, func(io.Reader) {})
	if !errors.Is(err, executor.ErrStart) {
		t.Fatalf("err = %v", err)
	}
}
