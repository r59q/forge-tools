// Command pipeprint compiles pipeline definitions between forge formats and
// Pipeprint, checks them and draws them as text.
//
//	pipeprint import .github/workflows/ci.yml > ci.pipeprint.yml
//	pipeprint export --to gitlab ci.pipeprint.yml > .gitlab-ci.yml
//	pipeprint convert --to forgejo .gitlab-ci.yml
//	pipeprint graph --steps .gitlab-ci.yml
package main

import (
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// version is set at release builds with -ldflags "-X main.version=v1.2.3".
var version = "dev" //nolint:gochecknoglobals // the linker can only set package variables.

// Exit codes.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

const usage = `pipeprint compiles pipelines between forges through Pipeprint.

Usage:
  pipeprint import  [--from FORMAT] [--json] [-o FILE] [--strict] [FILE]
  pipeprint export  --to FORMAT [-o FILE] [--strict] [FILE]
  pipeprint convert [--from FORMAT] --to FORMAT [-o FILE] [--strict] [FILE]
  pipeprint check   [--from FORMAT] [FILE]
  pipeprint graph   [--from FORMAT] [--steps] [--ascii] [--width N] [--json] [FILE]
  pipeprint formats
  pipeprint version

FILE defaults to standard input. Without --from, the format is detected.
Warnings about dropped settings go to standard error; --strict makes them
errors. Run "pipeprint formats" for the formats.
`

// run runs one command and returns its exit code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := &cli{stdin: stdin, stdout: stdout, stderr: stderr}
	if len(args) == 0 {
		c.print(stderr, usage)
		return exitUsage
	}

	commands := map[string]func([]string) int{
		"import":  c.importCmd,
		"export":  c.exportCmd,
		"convert": c.convertCmd,
		"check":   c.checkCmd,
		"graph":   c.graphCmd,
		"formats": c.formatsCmd,
	}

	if command, ok := commands[args[0]]; ok {
		return command(args[1:])
	}

	if args[0] == "version" || args[0] == "--version" {
		c.print(stdout, "pipeprint "+version+"\n")
		return exitOK
	}

	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		c.print(stdout, usage)
		return exitOK
	}

	c.errorf("unknown command %q", args[0])

	return exitUsage
}
