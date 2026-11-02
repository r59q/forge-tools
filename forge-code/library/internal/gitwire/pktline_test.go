package gitwire

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPktRoundTrip(t *testing.T) {
	var b bytes.Buffer
	pw := NewPktWriter(&b)
	pw.Line("hello")
	pw.Data([]byte{0, 1, 2})
	pw.Delim()
	pw.ResponseEnd()
	big := bytes.Repeat([]byte("x"), MaxPktData)
	pw.Data(big)
	if err := pw.Flush(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), "000ahello\n0007\x00\x01\x02") {
		t.Fatalf("encoding %q", b.String()[:20])
	}
	b.WriteString("rest")
	pr := NewPktReader(&b)
	want := []struct {
		kind PktKind
		data string
	}{
		{PktData, "hello\n"}, {PktData, "\x00\x01\x02"}, {PktDelim, ""}, {PktResponseEnd, ""}, {PktData, string(big)}, {PktFlush, ""},
	}
	for i, w := range want {
		kind, data, err := pr.Next()
		if err != nil || kind != w.kind || string(data) != w.data {
			t.Fatalf("pkt %d: %v %d %q", i, err, kind, data[:min(len(data), 10)])
		}
	}
	rest, _ := io.ReadAll(pr.Rest())
	if string(rest) != "rest" {
		t.Fatalf("rest %q", rest)
	}
	if _, _, err := NewPktReader(strings.NewReader("")).Next(); err != io.EOF {
		t.Fatalf("empty input: %v", err)
	}
}

func TestPktWriterErrors(t *testing.T) {
	pw := NewPktWriter(io.Discard)
	if err := pw.Data(nil); err == nil {
		t.Fatal("empty payload accepted")
	}
	if err := pw.Flush(); err == nil || pw.Err() == nil {
		t.Fatal("error not sticky")
	}
	if err := NewPktWriter(io.Discard).Data(make([]byte, MaxPktData+1)); err == nil {
		t.Fatal("oversized payload accepted")
	}
}

func TestPktReaderErrors(t *testing.T) {
	for _, in := range []string{"00", "0003", "000g", "fff1" + strings.Repeat("x", 65516), "0009abc", "zzzz"} {
		if _, _, err := NewPktReader(strings.NewReader(in)).Next(); !errors.Is(err, ErrProtocol) {
			t.Errorf("Next(%.10q) err = %v", in, err)
		}
	}
	// Upper-case hex and an empty data line are accepted.
	kind, data, err := NewPktReader(strings.NewReader("000Aabcdef")).Next()
	if err != nil || kind != PktData || string(data) != "abcdef" {
		t.Fatalf("upper hex: %v %q", err, data)
	}
	kind, data, err = NewPktReader(strings.NewReader("0004")).Next()
	if err != nil || kind != PktData || len(data) != 0 {
		t.Fatalf("empty line: %v %q", err, data)
	}
}

func TestDemux(t *testing.T) {
	var b bytes.Buffer
	pw := NewPktWriter(&b)
	pw.Data([]byte("\x01abc"))
	pw.Data([]byte("\x02progress"))
	pw.Data([]byte("\x01def"))
	pw.Flush()
	var out bytes.Buffer
	if err := demux(NewPktReader(bytes.NewReader(b.Bytes())), &out, 100); err != nil || out.String() != "abcdef" {
		t.Fatalf("demux: %v %q", err, out.String())
	}
	if err := demux(NewPktReader(bytes.NewReader(b.Bytes())), io.Discard, 5); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("cap: %v", err)
	}
	for in, want := range map[string]error{
		"000c\x03failed\n0000": ErrRemote,
		"000aERR no0000":       ErrRemote,
		"0006\x0900000":        ErrProtocol,
		"0004":                 ErrProtocol,
		"0006\x01a":            ErrProtocol,
		"00010000":             ErrProtocol,
	} {
		err := demux(NewPktReader(strings.NewReader(in)), io.Discard, 100)
		if !errors.Is(err, want) {
			t.Errorf("demux(%q) = %v, want %v", in, err, want)
		}
	}
	err := demux(NewPktReader(strings.NewReader("000c\x03failed\n0000")), io.Discard, 100)
	if !strings.Contains(err.Error(), "failed") {
		t.Fatalf("message lost: %v", err)
	}
}

func TestClean(t *testing.T) {
	if got := clean([]byte(" a\x1b[31mb\xff \n")); got != "a [31mb?" {
		t.Fatalf("clean = %q", got)
	}
	if got := clean(bytes.Repeat([]byte("é"), 400)); len(got) > maxMessage+3 || !strings.HasSuffix(got, "...") {
		t.Fatalf("long clean = %q", got)
	}
}
