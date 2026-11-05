package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxInput caps how much input is read.
const maxInput = 16 << 20

// cli holds the streams commands use.
type cli struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

func (c *cli) print(w io.Writer, text string) {
	_, _ = io.WriteString(w, text)
}

func (c *cli) errorf(format string, args ...any) {
	c.print(c.stderr, "pipeprint: "+fmt.Sprintf(format, args...)+"\n")
}

// flags returns a flag set that reports errors to stderr.
func (c *cli) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("pipeprint "+name, flag.ContinueOnError)
	fs.SetOutput(c.stderr)

	return fs
}

// parse reads flags anywhere among args and returns at most one file
// argument.
func (c *cli) parse(fs *flag.FlagSet, args []string) (string, bool) {
	var files []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", false
		}

		if fs.NArg() == 0 {
			break
		}

		files = append(files, fs.Arg(0))
		args = fs.Args()[1:]
	}

	if len(files) > 1 {
		c.errorf("give at most one file")
		return "", false
	}

	if len(files) == 0 {
		return "-", true
	}

	return files[0], true
}

// read returns the contents of file, or of stdin for "-".
func (c *cli) read(file string) ([]byte, error) {
	r := c.stdin
	if file != "-" {
		f, err := os.Open(file) //nolint:gosec // the user names the file to read.
		if err != nil {
			return nil, err
		}

		defer func() { _ = f.Close() }()
		r = f
	}

	data, err := io.ReadAll(io.LimitReader(r, maxInput+1))
	if len(data) > maxInput {
		return nil, errors.New("input is larger than 16 MiB")
	}

	return data, err
}

// write writes data to file, or to stdout when file is empty.
func (c *cli) write(file string, data []byte) error {
	if file == "" {
		_, err := c.stdout.Write(data)
		return err
	}

	return os.WriteFile(file, data, 0o644) //nolint:gosec // pipeline files are not secret and are shared with others.
}

// fail reports err and returns the failure exit code.
func (c *cli) fail(err error) int {
	c.errorf("%s", strings.TrimSpace(err.Error()))
	return exitFail
}
