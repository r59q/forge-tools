package gitwire

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// Fetch size caps.
const (
	maxV2Advertisement = 1 << 20
	maxFetchResponse   = 32 << 20
	maxFetchPack       = 16 << 20
)

// FetchCommit fetches one commit object from the repository at repoURL
// over protocol v2 upload-pack, without its trees or ancestors: want oid,
// deepen 1, filter tree:0. It returns the commit's content, checked
// against oid.
//
// A server without protocol v2, shallow fetches or filters gives
// ErrUnsupported. A server error, such as an unknown or unreachable oid,
// gives ErrRemote. HTTP failures are *forgehttp.Error.
func FetchCommit(ctx context.Context, hc *http.Client, repoURL string, oid gitobj.ID) ([]byte, error) {
	caps, err := advertiseV2(ctx, hc, repoURL)
	if err != nil {
		return nil, err
	}

	fetch, ok := caps.Value("fetch")
	if !ok {
		return nil, fmt.Errorf("%w: server has no v2 fetch", ErrUnsupported)
	}

	features := strings.Fields(fetch)
	for _, f := range []string{"shallow", "filter"} {
		if !slices.Contains(features, f) {
			return nil, fmt.Errorf("%w: server fetch lacks %q", ErrUnsupported, f)
		}
	}

	format, hasFormat := caps.Value("object-format")
	if hasFormat && format != "sha1" {
		return nil, fmt.Errorf("%w: object format %q", ErrUnsupported, clean([]byte(format)))
	}

	body, err := fetchRequest(oid, hasFormat)
	if err != nil {
		return nil, err
	}

	u, err := endpoint(repoURL, "git-upload-pack")
	if err != nil {
		return nil, err
	}

	req, err := newRequest(ctx, http.MethodPost, u, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", uploadPackRequest)
	req.Header.Set("Accept", uploadPackResult)
	req.Header.Set("Git-Protocol", "version=2")
	resp, err := do(hc, req, uploadPackResult)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	pack, err := readFetchResponse(resp.Body)
	if err != nil {
		return nil, err
	}

	objs, err := ReadPack(bytes.NewReader(pack), maxFetchPack)
	if err != nil {
		return nil, err
	}

	for _, o := range objs {
		if o.Type == gitobj.TypeCommit && o.ID() == oid {
			return o.Data, nil
		}
	}

	return nil, protoErr("commit %s not in fetched pack", oid)
}

// fetchRequest builds a v2 fetch of oid alone: deepen 1, filter tree:0.
func fetchRequest(oid gitobj.ID, hasFormat bool) (*bytes.Buffer, error) {
	caps := []string{"command=fetch"}
	if hasFormat {
		caps = append(caps, "object-format=sha1")
	}

	args := []string{"no-progress", "want " + oid.String(), "deepen 1", "filter tree:0", "done"}

	var body bytes.Buffer
	if err := writeV2Request(&body, caps, args); err != nil {
		return nil, err
	}

	return &body, nil
}

// writeV2Request writes a v2 command request: caps, a delim-pkt, args
// and a flush-pkt. caps starts with the "command=" line.
func writeV2Request(w io.Writer, caps, args []string) error {
	pw := NewPktWriter(w)
	if err := pw.Lines(caps...); err != nil {
		return err
	}
	if err := pw.Delim(); err != nil {
		return err
	}
	if err := pw.Lines(args...); err != nil {
		return err
	}

	return pw.Flush()
}

// advertiseV2 fetches and parses a protocol v2 upload-pack capability
// advertisement.
func advertiseV2(ctx context.Context, hc *http.Client, repoURL string) (Capabilities, error) {
	u, err := endpoint(repoURL, "info/refs")
	if err != nil {
		return nil, err
	}

	u.RawQuery = "service=git-upload-pack"
	req, err := newRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Git-Protocol", "version=2")
	resp, err := do(hc, req, uploadPackAdvertisement)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	return parseV2Advertisement(resp.Body)
}

// parseV2Advertisement parses "version 2" and the capability lines up to
// the flush. A leading "# service=" header is skipped, as some servers
// send it.
func parseV2Advertisement(r io.Reader) (Capabilities, error) {
	pr := NewPktReader(&countingReader{r: r, max: maxV2Advertisement})
	line, err := nextLine(pr)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(string(line), "# service=") {
		if err := skipToFlush(pr); err != nil {
			return nil, err
		}
		if line, err = nextLine(pr); err != nil {
			return nil, err
		}
	}

	if string(line) != "version 2" {
		return nil, fmt.Errorf("%w: server does not speak protocol v2", ErrUnsupported)
	}

	var caps Capabilities
	for {
		kind, data, err := pr.Next()
		if errors.Is(err, io.EOF) {
			return nil, protoErr("v2 advertisement ended without flush")
		}
		if err != nil {
			return nil, err
		}
		if kind == PktFlush {
			return caps, nil
		}
		if kind != PktData || len(trimLF(data)) == 0 {
			return nil, protoErr("bad v2 capability line")
		}

		caps = append(caps, string(trimLF(data)))
	}
}

// nextLine reads one data line without its newline. ERR lines give
// ErrRemote.
func nextLine(pr *PktReader) ([]byte, error) {
	kind, data, err := pr.Next()
	if errors.Is(err, io.EOF) {
		return nil, protoErr("unexpected end of response")
	}
	if err != nil {
		return nil, err
	}
	if kind != PktData {
		return nil, protoErr("unexpected special pkt-line")
	}

	line := trimLF(data)
	if bytes.HasPrefix(line, []byte("ERR ")) {
		return nil, remoteError(line[4:])
	}

	return line, nil
}

// readFetchResponse reads the sections of a v2 fetch response and returns
// the demultiplexed packfile.
func readFetchResponse(r io.Reader) ([]byte, error) {
	pr := NewPktReader(&countingReader{r: r, max: maxFetchResponse})
	for {
		header, err := nextLine(pr)
		if err != nil {
			return nil, err
		}

		switch string(header) {
		case "packfile":
			var pack bytes.Buffer
			if err := demux(pr, &pack, maxFetchPack); err != nil {
				return nil, err
			}
			return pack.Bytes(), nil
		case "acknowledgments", "shallow-info", "wanted-refs":
			if err := skipSection(pr); err != nil {
				return nil, err
			}
		default:
			return nil, protoErr("unexpected fetch section %q", clean(header))
		}
	}
}

// skipSection reads the lines of a section up to its delim-pkt.
func skipSection(pr *PktReader) error {
	for {
		kind, data, err := pr.Next()
		if errors.Is(err, io.EOF) {
			return protoErr("fetch section ended early")
		}
		if err != nil {
			return err
		}

		switch kind {
		case PktDelim:
			return nil
		case PktData:
			if bytes.HasPrefix(data, []byte("ERR ")) {
				return remoteError(trimLF(data[4:]))
			}
		default:
			return protoErr("fetch response ended without a packfile")
		}
	}
}
