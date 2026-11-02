package gitwire

import (
	"bytes"
	"errors"
	"io"
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
		if err == io.EOF {
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
	pw.Line("# service=git-receive-pack")
	pw.Flush()
	c := "\x00" + strings.Join(caps, " ")
	if len(refs) == 0 {
		pw.Line(gitobj.ZeroID.String() + " capabilities^{}" + c)
	}
	names := make([]string, 0, len(refs))
	for n := range refs {
		names = append(names, n)
	}
	slices.Sort(names)
	for i, n := range names {
		line := refs[n].String() + " " + n
		if i == 0 {
			line += c
		}
		pw.Line(line)
	}
	return pw.Flush()
}

// WriteReport writes r as report-status, with refs sorted by name. With
// sideband set it wraps the report in side-band channel 1.
func WriteReport(w io.Writer, r *Report, sideband bool) error {
	var buf bytes.Buffer
	pw := NewPktWriter(&buf)
	if r.UnpackOK {
		pw.Line("unpack ok")
	} else {
		msg := r.Unpack
		if msg == "" {
			msg = "error"
		}
		pw.Line("unpack " + msg)
	}
	names := make([]string, 0, len(r.Refs))
	for n := range r.Refs {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if st := r.Refs[n]; st.OK {
			pw.Line("ok " + n)
		} else {
			reason := st.Reason
			if reason == "" {
				reason = "rejected"
			}
			pw.Line("ng " + n + " " + reason)
		}
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

// writeSideband writes data on side-band channel 1, then a flush-pkt.
func writeSideband(w io.Writer, data []byte) error {
	pw := NewPktWriter(w)
	for len(data) > 0 {
		n := min(len(data), MaxPktData-1)
		pw.Data(append([]byte{bandData}, data[:n]...))
		data = data[n:]
	}
	return pw.Flush()
}

// WriteV2Advertisement writes a protocol v2 capability advertisement:
// "version 2", one line per capability, then a flush-pkt.
func WriteV2Advertisement(w io.Writer, caps []string) error {
	pw := NewPktWriter(w)
	pw.Line("version 2")
	for _, c := range caps {
		pw.Line(c)
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
		if err == io.EOF {
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
		pw.Line("shallow-info")
		for _, id := range shallow {
			pw.Line("shallow " + id.String())
		}
		pw.Delim()
	}
	if err := pw.Line("packfile"); err != nil {
		return err
	}
	return writeSideband(w, pack.Bytes())
}

// WriteError writes an "ERR msg" line, as servers do to refuse a request.
func WriteError(w io.Writer, msg string) error {
	if msg == "" || strings.ContainsAny(msg, "\n") {
		return errors.New("gitwire: bad error message")
	}
	return NewPktWriter(w).Line("ERR " + msg)
}
