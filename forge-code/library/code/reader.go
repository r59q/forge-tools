package code

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxLFSPointer is the size below which content may be an LFS pointer.
const maxLFSPointer = 1024

// finishReader caps r's body at max bytes and sets r.LFSPointer. It closes
// the body and returns an error if the known size is over max.
func finishReader(r *Reader, max int64) (*Reader, error) {
	if r.Size > max {
		r.Close()
		return nil, fmt.Errorf("%w: blob of %d bytes, limit %d", ErrTooLarge, r.Size, max)
	}
	body := bufio.NewReaderSize(r.ReadCloser, maxLFSPointer+16)
	if r.Size < maxLFSPointer {
		head, err := body.Peek(maxLFSPointer)
		if err != nil && !errors.Is(err, io.EOF) {
			r.Close()
			return nil, err
		}
		if len(head) < maxLFSPointer {
			r.Size = int64(len(head))
			r.LFSPointer = isLFSPointer(head)
		}
	}
	r.ReadCloser = &cappedBody{r: body, c: r.ReadCloser, left: max, max: max}
	return r, nil
}

// cappedBody returns at most max bytes and then fails with ErrTooLarge if
// more content follows.
type cappedBody struct {
	r         io.Reader
	c         io.Closer
	left, max int64
	err       error
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if b.left <= 0 {
		var one [1]byte
		n, err := b.r.Read(one[:])
		if n > 0 {
			b.err = fmt.Errorf("%w: blob over %d bytes", ErrTooLarge, b.max)
			return 0, b.err
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *cappedBody) Close() error { return b.c.Close() }

// isLFSPointer reports whether data is a Git LFS pointer file: a version
// line first, then "key value" lines including a sha256 oid and a size.
func isLFSPointer(data []byte) bool {
	if len(data) >= maxLFSPointer || !utf8.Valid(data) || !bytes.HasSuffix(data, []byte("\n")) {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if lines[0] != "version https://git-lfs.github.com/spec/v1" {
		return false
	}
	oid, size := false, false
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, " ")
		if !ok || key == "" || value == "" {
			return false
		}
		switch key {
		case "oid":
			hex, ok := strings.CutPrefix(value, "sha256:")
			oid = ok && len(hex) == 64 && OID(hex).Valid()
		case "size":
			n, err := strconv.ParseInt(value, 10, 64)
			size = err == nil && n >= 0
		}
	}
	return oid && size
}
