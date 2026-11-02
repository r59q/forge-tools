package gitwire

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

func sampleObjects() []gitobj.Object {
	blob := gitobj.Object{Type: gitobj.TypeBlob, Data: []byte("hello\n")}
	big := gitobj.Object{Type: gitobj.TypeBlob, Data: bytes.Repeat([]byte("0123456789"), 10000)}
	tree, _ := gitobj.EncodeTree([]gitobj.TreeEntry{{Name: "a", Mode: gitobj.ModeFile, ID: blob.ID()}})
	return []gitobj.Object{
		blob,
		{Type: gitobj.TypeBlob, Data: []byte{}},
		big,
		{Type: gitobj.TypeTree, Data: tree},
		{Type: gitobj.TypeCommit, Data: []byte("tree x\n\nm")},
		{Type: gitobj.TypeTag, Data: []byte("object x\n")},
	}
}

func TestPackRoundTrip(t *testing.T) {
	objs := sampleObjects()
	var b bytes.Buffer
	if err := WritePack(&b, objs); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	if string(data[:4]) != "PACK" || data[7] != 2 || data[11] != byte(len(objs)) {
		t.Fatalf("header % x", data[:12])
	}
	sum := sha1.Sum(data[:len(data)-20])
	if !bytes.Equal(sum[:], data[len(data)-20:]) {
		t.Fatal("bad trailer")
	}
	// Bytes after the pack stay unread with a bufio.Reader.
	br := bufio.NewReader(io.MultiReader(bytes.NewReader(data), strings.NewReader("tail")))
	got, err := ReadPack(br, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, objs) {
		t.Fatal("objects differ")
	}
	if tail, _ := io.ReadAll(br); string(tail) != "tail" {
		t.Fatalf("tail %q", tail)
	}

	b.Reset()
	if err := WritePack(&b, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadPack(&b, 0); err != nil || len(got) != 0 {
		t.Fatalf("empty pack: %v %v", got, err)
	}
	if err := WritePack(io.Discard, []gitobj.Object{{Type: "nope"}}); err == nil {
		t.Fatal("bad type accepted")
	}
}

func TestObjectHeader(t *testing.T) {
	for _, size := range []uint64{0, 15, 16, 127, 128, 1 << 20, 1<<40 + 3} {
		h := objectHeader(packBlob, size)
		code, got, err := readObjectHeader(bytes.NewReader(h))
		if err != nil || code != packBlob || got != size {
			t.Errorf("size %d: %d %d %v", size, code, got, err)
		}
	}
	if _, _, err := readObjectHeader(bytes.NewReader(bytes.Repeat([]byte{0xff}, 12))); !errors.Is(err, ErrProtocol) {
		t.Fatalf("overflow: %v", err)
	}
}

// rawPack builds a pack from raw object bytes, with a valid trailer.
func rawPack(count byte, body []byte) []byte {
	p := append([]byte{'P', 'A', 'C', 'K', 0, 0, 0, 2, 0, 0, 0, count}, body...)
	sum := sha1.Sum(p)
	return append(p, sum[:]...)
}

func zlibBytes(data []byte) []byte {
	var b bytes.Buffer
	zw := zlib.NewWriter(&b)
	zw.Write(data)
	zw.Close()
	return b.Bytes()
}

func TestReadPackErrors(t *testing.T) {
	var good bytes.Buffer
	WritePack(&good, sampleObjects()[:1])
	corrupt := bytes.Clone(good.Bytes())
	corrupt[len(corrupt)-1] ^= 1

	z := zlibBytes([]byte("abc"))
	for name, tc := range map[string]struct {
		in   []byte
		want error
	}{
		"checksum":   {corrupt, ErrProtocol},
		"magic":      {[]byte("PACX\x00\x00\x00\x02\x00\x00\x00\x00"), ErrProtocol},
		"version":    {[]byte("PACK\x00\x00\x00\x04\x00\x00\x00\x00"), ErrProtocol},
		"short":      {[]byte("PACK"), ErrProtocol},
		"truncated":  {good.Bytes()[:len(good.Bytes())-25], ErrProtocol},
		"ofs-delta":  {rawPack(1, append([]byte{packOfsDelta<<4 | 3}, z...)), ErrUnsupported},
		"ref-delta":  {rawPack(1, append([]byte{packRefDelta<<4 | 3}, z...)), ErrUnsupported},
		"type 5":     {rawPack(1, append([]byte{5<<4 | 3}, z...)), ErrProtocol},
		"too long":   {rawPack(1, append([]byte{packBlob<<4 | 2}, z...)), ErrProtocol},
		"too short":  {rawPack(1, append([]byte{packBlob<<4 | 4}, z...)), ErrProtocol},
		"not zlib":   {rawPack(1, []byte{packBlob<<4 | 3, 'a', 'b', 'c'}), ErrProtocol},
		"over limit": {rawPack(1, append([]byte{packBlob<<4 | 0x80 | 0x0f, 0x7f}, z...)), ErrTooLarge},
	} {
		if _, err := ReadPack(bytes.NewReader(tc.in), 1000); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}
