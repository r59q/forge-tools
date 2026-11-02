package gitwire

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// knownErr reports whether err is one a parser may return for bad input.
func knownErr(err error) bool {
	return err == nil || errors.Is(err, ErrProtocol) || errors.Is(err, ErrUnsupported) ||
		errors.Is(err, ErrRemote) || errors.Is(err, ErrTooLarge)
}

func FuzzPktLine(f *testing.F) {
	f.Add([]byte("000ahello\n00000001000200040006\x01x"))
	f.Add([]byte("fff0"))
	f.Add([]byte("0003"))
	f.Add([]byte("00"))
	f.Fuzz(func(t *testing.T, in []byte) {
		pr := NewPktReader(bytes.NewReader(in))
		var out bytes.Buffer
		pw := NewPktWriter(&out)
		for {
			kind, data, err := pr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("unexpected error type: %v", err)
				}
				return
			}
			if len(data) > MaxPktData {
				t.Fatalf("payload of %d bytes", len(data))
			}
			switch kind {
			case PktFlush:
				pw.Flush()
			case PktDelim:
				pw.Delim()
			case PktResponseEnd:
				pw.ResponseEnd()
			case PktData:
				if len(data) == 0 {
					return // "0004" has no writer form
				}
				pw.Data(data)
			}
		}
		// Valid input re-encodes to itself, up to hex case.
		if pw.Err() != nil || !bytes.EqualFold(out.Bytes(), in) {
			t.Fatalf("round trip: %q -> %q (%v)", in, out.Bytes(), pw.Err())
		}
	})
}

func FuzzReport(f *testing.F) {
	ok := pkt("unpack ok\n") + pkt("ok refs/heads/a\n") + pkt("ng refs/heads/b stale info\n") + "0000"
	f.Add([]byte(ok), false)
	f.Add([]byte(pkt("\x01"+ok)+pkt("\x02progress")+"0000"), true)
	f.Add([]byte(pkt("\x03error")), true)
	f.Add([]byte(pkt("unpack failed\n")+"0000"), false)
	f.Fuzz(func(t *testing.T, in []byte, sideband bool) {
		rep, err := readReport(bytes.NewReader(in), sideband)
		if !knownErr(err) {
			t.Fatalf("unexpected error type: %v", err)
		}
		if err != nil {
			return
		}
		for name, st := range rep.Refs {
			if name == "" || strings.ContainsAny(name, " \x00") || (!st.OK && st.Reason == "") {
				t.Fatalf("bad entry %q %+v", name, st)
			}
		}
		// What parsed writes back and parses the same.
		var b bytes.Buffer
		if err := WriteReport(&b, rep, sideband); err != nil {
			return // a name too long for one pkt-line
		}
		again, err := readReport(&b, sideband)
		if err != nil || again.UnpackOK != rep.UnpackOK || len(again.Refs) != len(rep.Refs) {
			t.Fatalf("round trip: %+v vs %+v (%v)", rep, again, err)
		}
	})
}

func FuzzPack(f *testing.F) {
	var b bytes.Buffer
	WritePack(&b, sampleObjects()[:2])
	f.Add(b.Bytes())
	b.Reset()
	WritePack(&b, nil)
	f.Add(b.Bytes())
	f.Add(rawPack(1, append([]byte{packRefDelta<<4 | 3}, zlibBytes([]byte("abc"))...)))
	f.Add([]byte("PACK\x00\x00\x00\x02\xff\xff\xff\xff"))
	f.Fuzz(func(t *testing.T, in []byte) {
		objs, err := ReadPack(bytes.NewReader(in), 1<<20)
		if !knownErr(err) {
			t.Fatalf("unexpected error type: %v", err)
		}
		if err != nil {
			return
		}
		var total int
		for _, o := range objs {
			if !o.Type.Valid() {
				t.Fatalf("type %q", o.Type)
			}
			total += len(o.Data)
		}
		if total > 1<<20 {
			t.Fatalf("read %d bytes past the cap", total)
		}
		var out bytes.Buffer
		if err := WritePack(&out, objs); err != nil {
			t.Fatal(err)
		}
		again, err := ReadPack(&out, 1<<20)
		if err != nil || len(again) != len(objs) {
			t.Fatalf("round trip: %v", err)
		}
	})
}

func FuzzAdvertisement(f *testing.F) {
	var b bytes.Buffer
	WriteAdvertisement(&b, map[string]gitobj.ID{"refs/heads/main": {1}, "refs/tags/v1": {2}}, []string{"report-status", "atomic"})
	f.Add(b.Bytes())
	b.Reset()
	WriteAdvertisement(&b, nil, []string{"report-status"})
	f.Add(b.Bytes())
	f.Add([]byte("001f# service=git-receive-pack\n0000000eERR denied"))
	f.Fuzz(func(t *testing.T, in []byte) {
		a, err := parseAdvertisement(bytes.NewReader(in))
		if !knownErr(err) {
			t.Fatalf("unexpected error type: %v", err)
		}
		if err != nil {
			return
		}
		for name := range a.Refs {
			if name == "" || strings.ContainsAny(name, " \x00\n") || name == ".have" || strings.HasSuffix(name, "^{}") {
				t.Fatalf("bad ref name %q", name)
			}
		}
		var out bytes.Buffer
		if err := WriteAdvertisement(&out, a.Refs, a.Caps); err != nil {
			return
		}
		again, err := parseAdvertisement(&out)
		if err != nil || len(again.Refs) != len(a.Refs) {
			t.Fatalf("round trip: %v", err)
		}
		for n, id := range a.Refs {
			if again.Refs[n] != id {
				t.Fatalf("ref %q changed", n)
			}
		}
	})
}
