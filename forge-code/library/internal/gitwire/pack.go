package gitwire

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// Pack object type codes.
const (
	packCommit   = 1
	packTree     = 2
	packBlob     = 3
	packTag      = 4
	packOfsDelta = 6
	packRefDelta = 7
)

var packTypes = map[gitobj.Type]byte{
	gitobj.TypeCommit: packCommit,
	gitobj.TypeTree:   packTree,
	gitobj.TypeBlob:   packBlob,
	gitobj.TypeTag:    packTag,
}

// WritePack writes objs as a version 2 pack: undeltified, zlib-compressed
// objects and a trailing SHA-1. No objects gives an empty pack.
func WritePack(w io.Writer, objs []gitobj.Object) error {
	if uint64(len(objs)) > 1<<32-1 {
		return errors.New("gitwire: too many objects for a pack")
	}
	h := sha1.New()
	bw := bufio.NewWriter(io.MultiWriter(w, h))
	var hdr [12]byte
	copy(hdr[:], "PACK")
	binary.BigEndian.PutUint32(hdr[4:], 2)
	binary.BigEndian.PutUint32(hdr[8:], uint32(len(objs)))
	bw.Write(hdr[:])
	zw := zlib.NewWriter(bw)
	for _, o := range objs {
		code, ok := packTypes[o.Type]
		if !ok {
			return fmt.Errorf("gitwire: bad object type %q", o.Type)
		}
		bw.Write(objectHeader(code, uint64(len(o.Data))))
		zw.Reset(bw)
		if _, err := zw.Write(o.Data); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	_, err := w.Write(h.Sum(nil))
	return err
}

// objectHeader encodes a pack object's type and size.
func objectHeader(code byte, size uint64) []byte {
	b := []byte{code<<4 | byte(size&0x0f)}
	size >>= 4
	for size > 0 {
		b[len(b)-1] |= 0x80
		b = append(b, byte(size&0x7f))
		size >>= 7
	}
	return b
}

// ReadPack reads a version 2 or 3 pack of undeltified objects and checks
// its trailing SHA-1. The objects' sizes may add up to at most maxBytes.
// A delta gives ErrUnsupported. If r is a *bufio.Reader, ReadPack reads
// no further than the pack's end.
func ReadPack(r io.Reader, maxBytes int64) ([]gitobj.Object, error) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	hr := &hashReader{r: br, h: sha1.New()}
	var hdr [12]byte
	if _, err := io.ReadFull(hr, hdr[:]); err != nil {
		return nil, packErr(err, "truncated pack header")
	}
	if string(hdr[:4]) != "PACK" {
		return nil, protoErr("not a pack")
	}
	if v := binary.BigEndian.Uint32(hdr[4:]); v != 2 && v != 3 {
		return nil, protoErr("pack version %d", v)
	}
	count := binary.BigEndian.Uint32(hdr[8:])
	var objs []gitobj.Object
	var total int64
	for i := uint32(0); i < count; i++ {
		code, size, err := readObjectHeader(hr)
		if err != nil {
			return nil, err
		}
		var typ gitobj.Type
		switch code {
		case packCommit:
			typ = gitobj.TypeCommit
		case packTree:
			typ = gitobj.TypeTree
		case packBlob:
			typ = gitobj.TypeBlob
		case packTag:
			typ = gitobj.TypeTag
		case packOfsDelta, packRefDelta:
			return nil, fmt.Errorf("%w: deltified pack objects", ErrUnsupported)
		default:
			return nil, protoErr("bad pack object type %d", code)
		}
		if size > uint64(maxBytes-total) {
			return nil, fmt.Errorf("%w: pack objects over %d bytes", ErrTooLarge, maxBytes)
		}
		total += int64(size)
		data, err := inflate(hr, int64(size))
		if err != nil {
			return nil, err
		}
		objs = append(objs, gitobj.Object{Type: typ, Data: data})
	}
	sum := hr.sum()
	var trailer [sha1.Size]byte
	if _, err := io.ReadFull(hr.r, trailer[:]); err != nil {
		return nil, packErr(err, "truncated pack trailer")
	}
	if !bytes.Equal(sum, trailer[:]) {
		return nil, protoErr("pack checksum mismatch")
	}
	return objs, nil
}

// readObjectHeader reads a pack object's type code and size.
func readObjectHeader(r io.ByteReader) (byte, uint64, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, 0, packErr(err, "truncated object header")
	}
	code := b >> 4 & 7
	size := uint64(b & 0x0f)
	for shift := uint(4); b&0x80 != 0; shift += 7 {
		if shift > 60 {
			return 0, 0, protoErr("object size overflows")
		}
		if b, err = r.ReadByte(); err != nil {
			return 0, 0, packErr(err, "truncated object header")
		}
		size |= uint64(b&0x7f) << shift
	}
	return code, size, nil
}

// inflate reads one zlib stream from r that must hold exactly size bytes.
// r must be a flate.Reader, so no bytes past the stream are consumed.
func inflate(r *hashReader, size int64) ([]byte, error) {
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, packErr(err, "bad zlib stream")
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(zr, size+1))
	if err != nil {
		return nil, packErr(err, "bad zlib stream")
	}
	if n != size {
		return nil, protoErr("object is %d bytes, header says %d", n, size)
	}
	return buf.Bytes(), nil
}

// packErr turns read errors into ErrProtocol errors, keeping size errors.
func packErr(err error, msg string) error {
	if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrRemote) {
		return err
	}
	return fmt.Errorf("%w: %s: %v", ErrProtocol, msg, err)
}

// hashReader hashes the bytes it returns. It implements io.ByteReader so
// that zlib does not read ahead.
type hashReader struct {
	r       *bufio.Reader
	h       hash.Hash
	pending []byte
}

func (h *hashReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.add(p[:n])
	return n, err
}

func (h *hashReader) ReadByte() (byte, error) {
	b, err := h.r.ReadByte()
	if err == nil {
		h.pending = append(h.pending, b)
		if len(h.pending) >= 4096 {
			h.add(nil)
		}
	}
	return b, err
}

func (h *hashReader) add(p []byte) {
	if len(h.pending) > 0 {
		h.h.Write(h.pending)
		h.pending = h.pending[:0]
	}
	h.h.Write(p)
}

func (h *hashReader) sum() []byte {
	h.add(nil)
	return h.h.Sum(nil)
}
