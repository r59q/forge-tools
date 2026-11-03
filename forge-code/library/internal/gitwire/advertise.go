package gitwire

import (
	"bytes"
	"context"
	"errors"
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

	p := &advertParser{a: &Advertisement{Refs: map[string]gitobj.ID{}}, first: true}
	for {
		kind, data, err := pr.Next()
		if errors.Is(err, io.EOF) {
			return nil, protoErr("advertisement ended without flush")
		}
		if err != nil {
			return nil, err
		}
		if kind == PktFlush {
			return p.a, nil
		}
		if kind != PktData {
			return nil, protoErr("unexpected special pkt-line in advertisement")
		}
		if err := p.line(trimLF(data)); err != nil {
			return nil, err
		}
	}
}

// advertParser collects the refs and capabilities of an advertisement.
type advertParser struct {
	a     *Advertisement
	first bool
}

// line parses one advertisement line.
func (p *advertParser) line(line []byte) error {
	if bytes.HasPrefix(line, []byte("ERR ")) {
		return remoteError(line[4:])
	}
	if p.first && string(line) == "version 1" {
		return nil
	}

	line, err := p.splitCaps(line)
	if err != nil {
		return err
	}

	id, name, err := parseRefLine(line)
	if err != nil {
		return err
	}

	return p.addRef(id, name)
}

// splitCaps reads the capabilities after the first ref and returns the
// line without them.
func (p *advertParser) splitCaps(line []byte) ([]byte, error) {
	if !p.first {
		if bytes.IndexByte(line, 0) >= 0 {
			return nil, protoErr("capabilities after the first ref")
		}

		return line, nil
	}

	p.first = false
	line, caps, _ := bytes.Cut(line, []byte{0})
	p.a.Caps = parseCaps(caps)
	if f, ok := p.a.Caps.Value("object-format"); ok && f != "sha1" {
		return nil, fmt.Errorf("%w: object format %q", ErrUnsupported, clean([]byte(f)))
	}

	return line, nil
}

// addRef records ref name, skipping ".have", peeled and capability lines.
func (p *advertParser) addRef(id gitobj.ID, name string) error {
	if name == "capabilities^{}" {
		if !id.IsZero() || len(p.a.Refs) > 0 {
			return protoErr("misplaced capabilities^{} line")
		}

		return nil
	}

	if name == ".have" || strings.HasSuffix(name, "^{}") {
		return nil
	}
	if _, dup := p.a.Refs[name]; dup {
		return protoErr("duplicate ref %q", clean([]byte(name)))
	}

	p.a.Refs[name] = id

	return nil
}

// skipServiceHeader reads the "# service=NAME" line and the flush after
// it.
func skipServiceHeader(pr *PktReader, service string) error {
	kind, data, err := pr.Next()
	if errors.Is(err, io.EOF) {
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
		if errors.Is(err, io.EOF) {
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
