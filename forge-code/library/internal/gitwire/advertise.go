package gitwire

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// maxAdvertisement caps the size of a receive-pack advertisement.
const maxAdvertisement = 64 << 20

// Capabilities is a list of capability tokens, such as "atomic" or
// "object-format=sha1".
type Capabilities []string

// Has reports whether c has the capability name, with or without a value.
func (c Capabilities) Has(name string) bool {
	_, ok := c.Value(name)
	return ok
}

// Value returns the value of capability name, "" if it has none, and
// whether c has it.
func (c Capabilities) Value(name string) (string, bool) {
	for _, s := range c {
		k, v, _ := strings.Cut(s, "=")
		if k == name {
			return v, true
		}
	}
	return "", false
}

// Advertisement is a receive-pack ref advertisement.
type Advertisement struct {
	// Refs maps full ref names to their values. ".have" lines and peeled
	// "^{}" lines are left out.
	Refs map[string]gitobj.ID
	// Caps are the server's capabilities.
	Caps Capabilities
}

// Advertise fetches the receive-pack advertisement of the repository at
// repoURL, such as "https://host/owner/repo.git", with GET
// info/refs?service=git-receive-pack. HTTP failures are
// *forgehttp.Error. A SHA-256 repository gives ErrUnsupported.
func Advertise(ctx context.Context, hc *http.Client, repoURL string) (*Advertisement, error) {
	u, err := endpoint(repoURL, "info/refs")
	if err != nil {
		return nil, err
	}
	u.RawQuery = "service=git-receive-pack"
	req, err := newRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := do(hc, req, receivePackAdvertisement)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return parseAdvertisement(resp.Body)
}

// parseAdvertisement parses a smart HTTP receive-pack advertisement.
func parseAdvertisement(r io.Reader) (*Advertisement, error) {
	pr := NewPktReader(&countingReader{r: r, max: maxAdvertisement})
	if err := skipServiceHeader(pr, "git-receive-pack"); err != nil {
		return nil, err
	}
	a := &Advertisement{Refs: map[string]gitobj.ID{}}
	first := true
	for {
		kind, data, err := pr.Next()
		if err == io.EOF {
			return nil, protoErr("advertisement ended without flush")
		}
		if err != nil {
			return nil, err
		}
		if kind == PktFlush {
			return a, nil
		}
		if kind != PktData {
			return nil, protoErr("unexpected special pkt-line in advertisement")
		}
		line := trimLF(data)
		if bytes.HasPrefix(line, []byte("ERR ")) {
			return nil, remoteError(line[4:])
		}
		if first && string(line) == "version 1" {
			continue
		}
		if first {
			first = false
			var caps []byte
			line, caps, _ = bytes.Cut(line, []byte{0})
			a.Caps = parseCaps(caps)
			if f, ok := a.Caps.Value("object-format"); ok && f != "sha1" {
				return nil, fmt.Errorf("%w: object format %q", ErrUnsupported, clean([]byte(f)))
			}
		} else if bytes.IndexByte(line, 0) >= 0 {
			return nil, protoErr("capabilities after the first ref")
		}
		id, name, err := parseRefLine(line)
		if err != nil {
			return nil, err
		}
		if name == "capabilities^{}" {
			if !id.IsZero() || len(a.Refs) > 0 {
				return nil, protoErr("misplaced capabilities^{} line")
			}
			continue
		}
		if name == ".have" || strings.HasSuffix(name, "^{}") {
			continue
		}
		if _, dup := a.Refs[name]; dup {
			return nil, protoErr("duplicate ref %q", clean([]byte(name)))
		}
		a.Refs[name] = id
	}
}

// skipServiceHeader reads the "# service=NAME" line and the flush after
// it.
func skipServiceHeader(pr *PktReader, service string) error {
	kind, data, err := pr.Next()
	if err == io.EOF {
		return protoErr("empty advertisement")
	}
	if err != nil {
		return err
	}
	if kind != PktData || string(trimLF(data)) != "# service="+service {
		if kind == PktData && bytes.HasPrefix(data, []byte("ERR ")) {
			return remoteError(trimLF(data[4:]))
		}
		return protoErr("not a smart HTTP %s advertisement", service)
	}
	return skipToFlush(pr)
}

// skipToFlush reads lines up to and including a flush-pkt, as Git does
// after the service header.
func skipToFlush(pr *PktReader) error {
	for {
		kind, _, err := pr.Next()
		if err == io.EOF {
			return protoErr("service header without flush")
		}
		if err != nil {
			return err
		}
		if kind == PktFlush {
			return nil
		}
	}
}

// parseRefLine parses "<oid> <name>".
func parseRefLine(line []byte) (gitobj.ID, string, error) {
	hex, name, ok := bytes.Cut(line, []byte{' '})
	if !ok || len(name) == 0 || bytes.ContainsAny(name, " \x00\n") {
		return gitobj.ID{}, "", protoErr("bad ref line %q", clean(line))
	}
	id, err := gitobj.ParseID(string(hex))
	if err != nil {
		return gitobj.ID{}, "", protoErr("bad ref line %q", clean(line))
	}
	return id, string(name), nil
}

// parseCaps splits a space-separated capability list.
func parseCaps(b []byte) Capabilities {
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return nil
	}
	return Capabilities(f)
}

// countingReader fails with ErrTooLarge after max bytes.
type countingReader struct {
	r   io.Reader
	n   int64
	max int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.max {
		return n, fmt.Errorf("%w: response over %d bytes", ErrTooLarge, c.max)
	}
	return n, err
}
