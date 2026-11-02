package gitwire

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// Pkt-line size limits.
const (
	// MaxPktLen is the largest pkt-line, length prefix included.
	MaxPktLen = 65520
	// MaxPktData is the largest pkt-line payload.
	MaxPktData = MaxPktLen - 4
)

// PktKind is the kind of a pkt-line.
type PktKind int

// Pkt-line kinds.
const (
	// PktData is a line with a payload.
	PktData PktKind = iota
	// PktFlush is "0000", the end of a list or message.
	PktFlush
	// PktDelim is "0001", the end of a section in protocol v2.
	PktDelim
	// PktResponseEnd is "0002", the end of a response in protocol v2.
	PktResponseEnd
)

// PktReader reads pkt-lines.
type PktReader struct {
	r   *bufio.Reader
	buf [MaxPktData]byte
}

// NewPktReader returns a PktReader that reads from r. It buffers r, so
// the bytes after the last pkt-line read are available from Rest.
func NewPktReader(r io.Reader) *PktReader {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, MaxPktLen)
	}
	return &PktReader{r: br}
}

// Next reads one pkt-line. The payload is only valid until the next call.
// It returns io.EOF only at a clean end of input, before a length prefix.
// Bad lengths and truncated lines give ErrProtocol.
func (p *PktReader) Next() (PktKind, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(p.r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, nil, io.EOF
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, protoErr("truncated pkt-line length")
		}
		return 0, nil, err
	}
	n := 0
	for _, c := range hdr {
		v, ok := hexVal(c)
		if !ok {
			return 0, nil, protoErr("bad pkt-line length %q", clean(hdr[:]))
		}
		n = n<<4 | v
	}
	switch {
	case n == 0:
		return PktFlush, nil, nil
	case n == 1:
		return PktDelim, nil, nil
	case n == 2:
		return PktResponseEnd, nil, nil
	case n < 4 || n > MaxPktLen:
		return 0, nil, protoErr("bad pkt-line length %d", n)
	}
	data := p.buf[:n-4]
	if _, err := io.ReadFull(p.r, data); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, protoErr("truncated pkt-line")
		}
		return 0, nil, err
	}
	return PktData, data, nil
}

// Rest returns a reader of the input after the pkt-lines read so far.
func (p *PktReader) Rest() io.Reader { return p.r }

func hexVal(c byte) (int, bool) {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0'), true
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10, true
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

// PktWriter writes pkt-lines. Errors are sticky: after one failure every
// call returns the same error.
type PktWriter struct {
	w   io.Writer
	err error
}

// NewPktWriter returns a PktWriter that writes to w.
func NewPktWriter(w io.Writer) *PktWriter { return &PktWriter{w: w} }

// Data writes b as one data line. b must be 1 to MaxPktData bytes.
func (p *PktWriter) Data(b []byte) error {
	if p.err != nil {
		return p.err
	}
	if len(b) == 0 || len(b) > MaxPktData {
		p.err = fmt.Errorf("gitwire: pkt-line payload of %d bytes", len(b))
		return p.err
	}
	var hdr [4]byte
	const digits = "0123456789abcdef"
	n := len(b) + 4
	for i := 3; i >= 0; i-- {
		hdr[i] = digits[n&0xf]
		n >>= 4
	}
	if _, p.err = p.w.Write(hdr[:]); p.err == nil {
		_, p.err = p.w.Write(b)
	}
	return p.err
}

// Line writes s followed by a newline as one data line.
func (p *PktWriter) Line(s string) error {
	return p.Data([]byte(s + "\n"))
}

// Flush writes a flush-pkt, "0000".
func (p *PktWriter) Flush() error { return p.special("0000") }

// Delim writes a delim-pkt, "0001".
func (p *PktWriter) Delim() error { return p.special("0001") }

// ResponseEnd writes a response-end-pkt, "0002".
func (p *PktWriter) ResponseEnd() error { return p.special("0002") }

// Err returns the first write error, if any.
func (p *PktWriter) Err() error { return p.err }

func (p *PktWriter) special(s string) error {
	if p.err == nil {
		_, p.err = io.WriteString(p.w, s)
	}
	return p.err
}

// trimLF drops one trailing newline.
func trimLF(b []byte) []byte {
	return bytes.TrimSuffix(b, []byte{'\n'})
}

// Side-band channels.
const (
	bandData     = 1
	bandProgress = 2
	bandError    = 3
)

// demux reads side-band lines until a flush-pkt and writes channel 1 to
// w, at most max bytes. It drops progress and turns channel 3 into an
// ErrRemote error.
func demux(pr *PktReader, w io.Writer, max int64) error {
	var n int64
	for {
		kind, data, err := pr.Next()
		if err == io.EOF {
			return protoErr("side-band stream ended without flush")
		}
		if err != nil {
			return err
		}
		switch kind {
		case PktFlush:
			return nil
		case PktData:
		default:
			return protoErr("unexpected special pkt-line in side-band stream")
		}
		if len(data) == 0 {
			return protoErr("empty side-band line")
		}
		switch data[0] {
		case bandData:
			n += int64(len(data) - 1)
			if n > max {
				return fmt.Errorf("%w: side-band data over %d bytes", ErrTooLarge, max)
			}
			if _, err := w.Write(data[1:]); err != nil {
				return err
			}
		case bandProgress:
		case bandError:
			return remoteError(data[1:])
		default:
			if bytes.HasPrefix(data, []byte("ERR ")) {
				return remoteError(data[4:])
			}
			return protoErr("bad side-band channel %d", data[0])
		}
	}
}
