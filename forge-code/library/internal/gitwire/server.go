package gitwire

import (
	"bytes"
	"cmp"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// This file holds the server side of the protocol, for fake forges in
// tests. Handlers set the Content-Type themselves.

// ParseCommands reads a receive-pack request body: the commands, the
// client's capabilities and a reader of the rest of the body, which holds
// the pack if one was sent. The reader is a *bufio.Reader, so ReadPack
// stops at the pack's end.
func ParseCommands(r io.Reader) ([]Command, Capabilities, io.Reader, error) {
	pr := NewPktReader(r)
	var cmds []Command
	var caps Capabilities
	for {
		kind, data, err := pr.Next()
		if errors.Is(err, io.EOF) {
			return nil, nil, nil, protoErr("command list ended without flush")
		}
		if err != nil {
			return nil, nil, nil, err
		}
		if kind == PktFlush {
			break
		}
		if kind != PktData {
			return nil, nil, nil, protoErr("unexpected special pkt-line in commands")
		}

		line := trimLF(data)
		if len(cmds) == 0 {
			var c []byte
			line, c, _ = bytes.Cut(line, []byte{0})
			caps = parseCaps(c)
		}

		f := strings.Split(string(line), " ")
		if len(f) != 3 {
			return nil, nil, nil, protoErr("bad command %q", clean(line))
		}

		old, err1 := gitobj.ParseID(f[0])
		nw, err2 := gitobj.ParseID(f[1])
		if err1 != nil || err2 != nil || !validRefName(f[2]) {
			return nil, nil, nil, protoErr("bad command %q", clean(line))
		}

		cmds = append(cmds, Command{Name: f[2], Old: old, New: nw})
	}

	return cmds, caps, pr.Rest(), nil
}

// WriteAdvertisement writes a smart HTTP receive-pack advertisement: the
// service header, refs sorted by name with caps on the first, or the
// "capabilities^{}" line when there are no refs.
func WriteAdvertisement(w io.Writer, refs map[string]gitobj.ID, caps []string) error {
	pw := NewPktWriter(w)
	if err := pw.Line("# service=git-receive-pack"); err != nil {
		return err
	}
	if err := pw.Flush(); err != nil {
		return err
	}
	if err := pw.Lines(advertisedRefs(refs, caps)...); err != nil {
		return err
	}

	return pw.Flush()
}

// advertisedRefs returns the ref lines of an advertisement, with caps
// after a NUL on the first.
func advertisedRefs(refs map[string]gitobj.ID, caps []string) []string {
	c := "\x00" + strings.Join(caps, " ")
	if len(refs) == 0 {
		return []string{gitobj.ID{}.String() + " capabilities^{}" + c}
	}

	lines := make([]string, 0, len(refs))
	for _, n := range sortedKeys(refs) {
		lines = append(lines, refs[n].String()+" "+n)
	}

	lines[0] += c

	return lines
}

// WriteReport writes r as report-status, with refs sorted by name. With
// sideband set it wraps the report in side-band channel 1.
func WriteReport(w io.Writer, r *Report, sideband bool) error {
	var buf bytes.Buffer
	pw := NewPktWriter(&buf)
	if err := pw.Lines(reportLines(r)...); err != nil {
		return err
	}
	if err := pw.Flush(); err != nil {
		return err
	}
	if !sideband {
		_, err := w.Write(buf.Bytes())
		return err
	}

	return writeSideband(w, buf.Bytes())
}

// reportLines returns the report-status lines of r.
func reportLines(r *Report) []string {
	unpack := "ok"
	if !r.UnpackOK {
		unpack = cmp.Or(r.Unpack, "error")
	}

	lines := []string{"unpack " + unpack}
	for _, n := range sortedKeys(r.Refs) {
		if st := r.Refs[n]; st.OK {
			lines = append(lines, "ok "+n)
		} else {
			lines = append(lines, "ng "+n+" "+cmp.Or(st.Reason, "rejected"))
		}
	}

	return lines
}

// sortedKeys returns the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// writeSideband writes data on side-band channel 1, then a flush-pkt.
func writeSideband(w io.Writer, data []byte) error {
	pw := NewPktWriter(w)
	for len(data) > 0 {
		n := min(len(data), MaxPktData-1)
		if err := pw.Data(append([]byte{bandData}, data[:n]...)); err != nil {
			return err
		}

		data = data[n:]
	}

	return pw.Flush()
}

// WriteV2Advertisement writes a protocol v2 capability advertisement:
// "version 2", one line per capability, then a flush-pkt.
func WriteV2Advertisement(w io.Writer, caps []string) error {
	pw := NewPktWriter(w)
	if err := pw.Line("version 2"); err != nil {
		return err
	}
	if err := pw.Lines(caps...); err != nil {
		return err
	}

	return pw.Flush()
}

// V2Request is a protocol v2 command request.
type V2Request struct {
	// Command is the value of the "command=" line, such as "fetch".
	Command string
	// Caps are the capability lines before the delim-pkt.
	Caps Capabilities
	// Args are the argument lines after it, without newlines.
	Args []string
}

// ParseV2Request reads a protocol v2 command request.
func ParseV2Request(r io.Reader) (*V2Request, error) {
	pr := NewPktReader(r)
	req := &V2Request{}
	args := false
	for {
		kind, data, err := pr.Next()
		if errors.Is(err, io.EOF) {
			return nil, protoErr("v2 request ended without flush")
		}
		if err != nil {
			return nil, err
		}

		switch kind {
		case PktFlush:
			if req.Command == "" {
				return nil, protoErr("v2 request has no command")
			}
			return req, nil
		case PktDelim:
			if args {
				return nil, protoErr("second delim in v2 request")
			}
			args = true
			continue
		case PktResponseEnd:
			return nil, protoErr("response-end in v2 request")
		}

		line := string(trimLF(data))
		switch {
		case args:
			req.Args = append(req.Args, line)
		case req.Command == "":
			cmd, ok := strings.CutPrefix(line, "command=")
			if !ok || cmd == "" {
				return nil, protoErr("v2 request does not start with command=")
			}
			req.Command = cmd
		default:
			req.Caps = append(req.Caps, line)
		}
	}
}

// WriteFetchResponse writes a v2 fetch response that holds objs as an
// undeltified pack: a "shallow-info" section if shallow is not empty, then
// the "packfile" section on side-band channel 1, then a flush-pkt.
func WriteFetchResponse(w io.Writer, shallow []gitobj.ID, objs []gitobj.Object) error {
	var pack bytes.Buffer
	if err := WritePack(&pack, objs); err != nil {
		return err
	}

	pw := NewPktWriter(w)
	if len(shallow) > 0 {
		if err := writeShallowInfo(pw, shallow); err != nil {
			return err
		}
	}

	if err := pw.Line("packfile"); err != nil {
		return err
	}

	return writeSideband(w, pack.Bytes())
}

// writeShallowInfo writes a "shallow-info" section and its delim-pkt.
func writeShallowInfo(pw *PktWriter, shallow []gitobj.ID) error {
	lines := []string{"shallow-info"}
	for _, id := range shallow {
		lines = append(lines, "shallow "+id.String())
	}

	if err := pw.Lines(lines...); err != nil {
		return err
	}

	return pw.Delim()
}

// WriteError writes an "ERR msg" line, as servers do to refuse a request.
func WriteError(w io.Writer, msg string) error {
	if msg == "" || strings.ContainsAny(msg, "\n") {
		return errors.New("gitwire: bad error message")
	}

	return NewPktWriter(w).Line("ERR " + msg)
}
