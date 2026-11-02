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

// maxReport caps the report-status data of a push response, and
// maxPushResponse the whole response, progress included.
const (
	maxReport       = 1 << 20
	maxPushResponse = 64 << 20
)

// Command is one ref update in a push. Old is the value the ref must have,
// ZeroID if it must not exist. New ZeroID deletes the ref.
type Command struct {
	Name string
	Old  gitobj.ID
	New  gitobj.ID
}

// RefStatus is the server's verdict on one command.
type RefStatus struct {
	// OK is true for "ok", false for "ng".
	OK bool
	// Reason is the server's reason for "ng".
	Reason string
}

// Report is a parsed report-status.
type Report struct {
	// UnpackOK is true if the server unpacked the pack.
	UnpackOK bool
	// Unpack is the server's unpack error when UnpackOK is false.
	Unpack string
	// Refs maps ref names to their status. A command the server did not
	// report on is missing.
	Refs map[string]RefStatus
}

// Push sends cmds and pack to the receive-pack endpoint of repoURL and
// returns the server's report. caps are sent verbatim and must include
// "report-status"; with "side-band-64k" or "side-band" the response is
// demultiplexed. A nil pack is sent as nothing when every command is a
// delete, and as an empty pack otherwise. A pack with a Len method, such
// as *bytes.Buffer, is sent with a Content-Length; others are chunked.
//
// An "ng" result is not an error: check the Report. HTTP failures are
// *forgehttp.Error. Any error after the request was sent leaves the
// outcome unknown.
func Push(ctx context.Context, hc *http.Client, repoURL string, cmds []Command, caps []string, pack io.Reader) (*Report, error) {
	sideband, err := checkPush(cmds, caps)
	if err != nil {
		return nil, err
	}
	u, err := endpoint(repoURL, "git-receive-pack")
	if err != nil {
		return nil, err
	}
	var head bytes.Buffer
	if err := writeCommands(&head, cmds, caps); err != nil {
		return nil, err
	}
	if pack == nil && !allDeletes(cmds) {
		var empty bytes.Buffer
		if err := WritePack(&empty, nil); err != nil {
			return nil, err
		}
		pack = &empty
	}
	body, size := io.Reader(&head), int64(head.Len())
	if pack != nil {
		body = io.MultiReader(&head, pack)
		size = -1
		// Send a length when it is known; some servers refuse chunked bodies.
		if l, ok := pack.(interface{ Len() int }); ok {
			size = int64(head.Len() + l.Len())
		}
	}
	req, err := newRequest(ctx, http.MethodPost, u, body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", receivePackRequest)
	req.Header.Set("Accept", receivePackResult)
	resp, err := do(hc, req, receivePackResult)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readReport(resp.Body, sideband)
}

// checkPush validates the commands and capabilities of a push and reports
// whether side-band is requested.
func checkPush(cmds []Command, caps []string) (bool, error) {
	if len(cmds) == 0 {
		return false, errors.New("gitwire: push without commands")
	}
	seen := map[string]bool{}
	for _, c := range cmds {
		if !validRefName(c.Name) {
			return false, fmt.Errorf("gitwire: bad ref name %q", c.Name)
		}
		if seen[c.Name] {
			return false, fmt.Errorf("gitwire: duplicate command for %q", c.Name)
		}
		seen[c.Name] = true
		if c.Old.IsZero() && c.New.IsZero() {
			return false, fmt.Errorf("gitwire: command for %q has zero old and new", c.Name)
		}
	}
	report, sideband := false, false
	for _, c := range caps {
		if c == "" || strings.ContainsFunc(c, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return false, fmt.Errorf("gitwire: bad capability %q", c)
		}
		switch c {
		case "report-status":
			report = true
		case "side-band-64k", "side-band":
			sideband = true
		case "report-status-v2", "push-options", "push-cert":
			return false, fmt.Errorf("gitwire: capability %q is not supported", c)
		}
	}
	if !report {
		return false, errors.New("gitwire: push needs the report-status capability")
	}
	return sideband, nil
}

// validRefName reports whether name is a "refs/" name that is safe on the
// wire. Full check-ref-format rules are the caller's job.
func validRefName(name string) bool {
	if !strings.HasPrefix(name, "refs/") || len(name) > 1024 {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

func allDeletes(cmds []Command) bool {
	for _, c := range cmds {
		if !c.New.IsZero() {
			return false
		}
	}
	return true
}

// writeCommands writes the command list of a receive-pack request, with
// caps after a NUL on the first line, then a flush-pkt.
func writeCommands(w io.Writer, cmds []Command, caps []string) error {
	pw := NewPktWriter(w)
	for i, c := range cmds {
		line := c.Old.String() + " " + c.New.String() + " " + c.Name
		if i == 0 {
			line += "\x00" + strings.Join(caps, " ")
		}
		pw.Line(line)
	}
	return pw.Flush()
}

// readReport reads a push response, demultiplexing side-band if set.
func readReport(r io.Reader, sideband bool) (*Report, error) {
	r = &countingReader{r: r, max: maxPushResponse}
	if !sideband {
		return parseReport(io.LimitReader(r, maxReport))
	}
	var buf bytes.Buffer
	if err := demux(NewPktReader(r), &buf, maxReport); err != nil {
		return nil, err
	}
	return parseReport(&buf)
}

// parseReport parses report-status lines up to the flush-pkt.
func parseReport(r io.Reader) (*Report, error) {
	pr := NewPktReader(r)
	next := func() ([]byte, bool, error) {
		kind, data, err := pr.Next()
		if err == io.EOF {
			return nil, false, protoErr("report-status ended without flush")
		}
		if err != nil {
			return nil, false, err
		}
		switch kind {
		case PktFlush:
			return nil, true, nil
		case PktData:
			return trimLF(data), false, nil
		}
		return nil, false, protoErr("unexpected special pkt-line in report-status")
	}
	line, end, err := next()
	if err != nil {
		return nil, err
	}
	if end {
		return nil, protoErr("empty report-status")
	}
	if bytes.HasPrefix(line, []byte("ERR ")) {
		return nil, remoteError(line[4:])
	}
	status, ok := bytes.CutPrefix(line, []byte("unpack "))
	if !ok {
		return nil, protoErr("report-status has no unpack line")
	}
	rep := &Report{UnpackOK: string(status) == "ok", Refs: map[string]RefStatus{}}
	if !rep.UnpackOK {
		rep.Unpack = clean(status)
		if rep.Unpack == "" || rep.Unpack == "ok" {
			rep.Unpack = "malformed unpack status"
		}
	}
	for {
		line, end, err := next()
		if err != nil {
			return nil, err
		}
		if end {
			return rep, nil
		}
		var st RefStatus
		var name []byte
		switch {
		case bytes.HasPrefix(line, []byte("ok ")):
			st.OK = true
			name = line[3:]
		case bytes.HasPrefix(line, []byte("ng ")):
			var reason []byte
			name, reason, _ = bytes.Cut(line[3:], []byte{' '})
			st.Reason = clean(reason)
			if st.Reason == "" {
				st.Reason = "rejected"
			}
		default:
			return nil, protoErr("bad report-status line %q", clean(line))
		}
		if len(name) == 0 || bytes.ContainsAny(name, " \x00") {
			return nil, protoErr("bad report-status line %q", clean(line))
		}
		if _, dup := rep.Refs[string(name)]; dup {
			return nil, protoErr("duplicate report for %q", clean(name))
		}
		rep.Refs[string(name)] = st
	}
}
