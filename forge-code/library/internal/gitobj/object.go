// Package gitobj encodes and decodes Git objects in SHA-1 repositories:
// blobs, trees, commits and tags. It computes object IDs and sorts tree
// entries the way Git does.
package gitobj

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// ErrInvalid is wrapped by errors for malformed objects and bad input.
var ErrInvalid = errors.New("gitobj: invalid object")

// Size is the length of a SHA-1 object ID in bytes.
const Size = sha1.Size

// ID is a SHA-1 object ID.
type ID [Size]byte

// ZeroID is the all-zero ID that Git uses for "no object".
var ZeroID ID

// ParseID parses an ID written as 40 lowercase hex digits.
func ParseID(s string) (ID, error) {
	var id ID
	if len(s) != 2*Size {
		return id, fmt.Errorf("%w: object ID %s is not 40 hex digits", ErrInvalid, quote(s))
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return id, fmt.Errorf("%w: object ID %s is not lowercase hex", ErrInvalid, quote(s))
		}
	}
	hex.Decode(id[:], []byte(s))
	return id, nil
}

// String returns the ID as 40 lowercase hex digits.
func (id ID) String() string { return hex.EncodeToString(id[:]) }

// IsZero reports whether id is ZeroID.
func (id ID) IsZero() bool { return id == ZeroID }

// Type is a Git object type.
type Type string

// Object types.
const (
	TypeBlob   Type = "blob"
	TypeTree   Type = "tree"
	TypeCommit Type = "commit"
	TypeTag    Type = "tag"
)

// Valid reports whether t is one of the four object types.
func (t Type) Valid() bool {
	switch t {
	case TypeBlob, TypeTree, TypeCommit, TypeTag:
		return true
	}
	return false
}

// Hash returns the ID of an object with type t and content data.
func Hash(t Type, data []byte) ID {
	h := sha1.New()
	h.Write([]byte(string(t) + " " + strconv.Itoa(len(data)) + "\x00"))
	h.Write(data)
	var id ID
	h.Sum(id[:0])
	return id
}

// Object is an encoded object: its type and content, without the header.
type Object struct {
	Type Type
	Data []byte
}

// ID returns the object's ID.
func (o Object) ID() ID { return Hash(o.Type, o.Data) }

// quote quotes s for an error message, cut to 64 bytes.
func quote(s string) string {
	if len(s) > 64 {
		return strconv.Quote(s[:64]) + "..."
	}
	return strconv.Quote(s)
}
