package gitobj

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Signature is an author, committer or tagger.
type Signature struct {
	Name  string
	Email string
	When  time.Time
}

// Encode returns s as Git writes it: "Name <email> unix +hhmm". The zone
// offset of When is kept, cut to whole minutes. It rejects an empty name,
// "<", ">", newline or NUL in the name or email, times before 1970 and
// offsets of 100 hours or more.
func (s Signature) Encode() (string, error) {
	if s.Name == "" {
		return "", fmt.Errorf("%w: empty signature name", ErrInvalid)
	}
	if strings.ContainsAny(s.Name, "<>\n\x00") || strings.ContainsAny(s.Email, "<>\n\x00") {
		return "", fmt.Errorf("%w: signature has <, >, newline or NUL", ErrInvalid)
	}
	sec := s.When.Unix()
	if sec < 0 {
		return "", fmt.Errorf("%w: signature time before 1970", ErrInvalid)
	}
	_, off := s.When.Zone()
	sign := byte('+')
	if off < 0 {
		sign, off = '-', -off
	}
	mins := off / 60
	if mins >= 100*60 {
		return "", fmt.Errorf("%w: signature zone offset too large", ErrInvalid)
	}
	return fmt.Sprintf("%s <%s> %d %c%02d%02d", s.Name, s.Email, sec, sign, mins/60, mins%60), nil
}

// ParseSignature parses "Name <email> unix +hhmm". When gets a fixed zone
// with the stored offset.
func ParseSignature(s string) (Signature, error) {
	bad := fmt.Errorf("%w: bad signature %s", ErrInvalid, quote(s))
	lt := strings.IndexByte(s, '<')
	if lt < 0 {
		return Signature{}, bad
	}
	gt := strings.IndexByte(s[lt+1:], '>')
	if gt < 0 {
		return Signature{}, bad
	}
	gt += lt + 1
	f := strings.Fields(s[gt+1:])
	if len(f) != 2 {
		return Signature{}, bad
	}
	sec, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil || sec < 0 {
		return Signature{}, bad
	}
	tz := f[1]
	if len(tz) != 5 || (tz[0] != '+' && tz[0] != '-') {
		return Signature{}, bad
	}
	hhmm, err := strconv.Atoi(tz[1:])
	if err != nil || hhmm < 0 || hhmm%100 >= 60 {
		return Signature{}, bad
	}
	off := (hhmm/100*60 + hhmm%100) * 60
	if tz[0] == '-' {
		off = -off
	}
	return Signature{
		Name:  strings.TrimSpace(s[:lt]),
		Email: s[lt+1 : gt],
		When:  time.Unix(sec, 0).In(time.FixedZone("", off)),
	}, nil
}

// Header is an extra commit or tag header, such as "gpgsig" or
// "encoding". Value may span lines.
type Header struct {
	Key   string
	Value string
}

// Commit is a commit object.
type Commit struct {
	Tree      ID
	Parents   []ID
	Author    Signature
	Committer Signature
	// Extra holds headers after the committer, in order.
	Extra   []Header
	Message string
}

// EncodeCommit returns the content of c. The message is written as is,
// but must not hold NUL, which git fsck rejects.
func EncodeCommit(c Commit) ([]byte, error) {
	if strings.Contains(c.Message, "\x00") {
		return nil, fmt.Errorf("%w: NUL in commit message", ErrInvalid)
	}
	var b bytes.Buffer
	writeHeader(&b, "tree", c.Tree.String())
	for _, p := range c.Parents {
		writeHeader(&b, "parent", p.String())
	}
	if err := writeSignature(&b, "author", c.Author); err != nil {
		return nil, err
	}
	if err := writeSignature(&b, "committer", c.Committer); err != nil {
		return nil, err
	}
	if err := writeExtra(&b, c.Extra, "tree", "parent", "author", "committer"); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	b.WriteString(c.Message)
	return b.Bytes(), nil
}

// DecodeCommit parses commit content. Headers must come in Git's order:
// tree, parents, author, committer, then any extra headers.
func DecodeCommit(data []byte) (Commit, error) {
	hs, msg, err := parseHeaders(data)
	if err != nil {
		return Commit{}, err
	}
	var c Commit
	next := func(key string) (string, error) {
		if len(hs) == 0 || hs[0].Key != key {
			return "", fmt.Errorf("%w: commit has no %s header", ErrInvalid, key)
		}
		v := hs[0].Value
		hs = hs[1:]
		return v, nil
	}
	v, err := next("tree")
	if err != nil {
		return Commit{}, err
	}
	if c.Tree, err = ParseID(v); err != nil {
		return Commit{}, err
	}
	for len(hs) > 0 && hs[0].Key == "parent" {
		v, _ := next("parent")
		p, err := ParseID(v)
		if err != nil {
			return Commit{}, err
		}
		c.Parents = append(c.Parents, p)
	}
	if v, err = next("author"); err != nil {
		return Commit{}, err
	}
	if c.Author, err = ParseSignature(v); err != nil {
		return Commit{}, err
	}
	if v, err = next("committer"); err != nil {
		return Commit{}, err
	}
	if c.Committer, err = ParseSignature(v); err != nil {
		return Commit{}, err
	}
	if c.Extra, err = extra(hs, "tree", "parent", "author", "committer"); err != nil {
		return Commit{}, err
	}
	c.Message = msg
	return c, nil
}

// Tag is an annotated tag object.
type Tag struct {
	Object ID
	Type   Type
	Name   string
	// Tagger is nil for old tags that have none.
	Tagger *Signature
	// Extra holds headers after the tagger, in order.
	Extra   []Header
	Message string
}

// EncodeTag returns the content of t. The message is written as is, but
// must not hold NUL.
func EncodeTag(t Tag) ([]byte, error) {
	if strings.Contains(t.Message, "\x00") {
		return nil, fmt.Errorf("%w: NUL in tag message", ErrInvalid)
	}
	if !t.Type.Valid() {
		return nil, fmt.Errorf("%w: tag target type %s", ErrInvalid, quote(string(t.Type)))
	}
	if t.Name == "" || strings.ContainsAny(t.Name, "\n\x00") {
		return nil, fmt.Errorf("%w: tag name %s", ErrInvalid, quote(t.Name))
	}
	var b bytes.Buffer
	writeHeader(&b, "object", t.Object.String())
	writeHeader(&b, "type", string(t.Type))
	writeHeader(&b, "tag", t.Name)
	if t.Tagger != nil {
		if err := writeSignature(&b, "tagger", *t.Tagger); err != nil {
			return nil, err
		}
	}
	if err := writeExtra(&b, t.Extra, "object", "type", "tag", "tagger"); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	b.WriteString(t.Message)
	return b.Bytes(), nil
}

// DecodeTag parses tag content: object, type, tag, an optional tagger,
// then any extra headers.
func DecodeTag(data []byte) (Tag, error) {
	hs, msg, err := parseHeaders(data)
	if err != nil {
		return Tag{}, err
	}
	var t Tag
	for i, key := range []string{"object", "type", "tag"} {
		if len(hs) <= i || hs[i].Key != key {
			return Tag{}, fmt.Errorf("%w: tag has no %s header", ErrInvalid, key)
		}
	}
	if t.Object, err = ParseID(hs[0].Value); err != nil {
		return Tag{}, err
	}
	t.Type = Type(hs[1].Value)
	if !t.Type.Valid() {
		return Tag{}, fmt.Errorf("%w: tag target type %s", ErrInvalid, quote(hs[1].Value))
	}
	t.Name = hs[2].Value
	if t.Name == "" || strings.Contains(t.Name, "\n") {
		return Tag{}, fmt.Errorf("%w: tag name %s", ErrInvalid, quote(t.Name))
	}
	hs = hs[3:]
	if len(hs) > 0 && hs[0].Key == "tagger" {
		sig, err := ParseSignature(hs[0].Value)
		if err != nil {
			return Tag{}, err
		}
		t.Tagger = &sig
		hs = hs[1:]
	}
	if t.Extra, err = extra(hs, "object", "type", "tag", "tagger"); err != nil {
		return Tag{}, err
	}
	t.Message = msg
	return t, nil
}

func writeHeader(b *bytes.Buffer, key, value string) {
	b.WriteString(key)
	b.WriteByte(' ')
	b.WriteString(strings.ReplaceAll(value, "\n", "\n "))
	b.WriteByte('\n')
}

func writeSignature(b *bytes.Buffer, key string, s Signature) error {
	v, err := s.Encode()
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	writeHeader(b, key, v)
	return nil
}

// writeExtra writes extra headers. Keys must be single words other than
// the reserved ones; values must not hold NUL.
func writeExtra(b *bytes.Buffer, hs []Header, reserved ...string) error {
	for _, h := range hs {
		if h.Key == "" || strings.ContainsAny(h.Key, " \n\x00") || strings.Contains(h.Value, "\x00") {
			return fmt.Errorf("%w: bad header %s", ErrInvalid, quote(h.Key))
		}
		for _, r := range reserved {
			if h.Key == r {
				return fmt.Errorf("%w: reserved header %s", ErrInvalid, quote(h.Key))
			}
		}
		writeHeader(b, h.Key, h.Value)
	}
	return nil
}

// extra checks that no header in hs uses a reserved key.
func extra(hs []Header, reserved ...string) ([]Header, error) {
	for _, h := range hs {
		for _, r := range reserved {
			if h.Key == r {
				return nil, fmt.Errorf("%w: misplaced %s header", ErrInvalid, r)
			}
		}
	}
	if len(hs) == 0 {
		return nil, nil
	}
	return hs, nil
}

// parseHeaders splits commit or tag content into headers and message.
// Lines that start with a space continue the previous header. The
// headers end at the first empty line, or at the end of data.
func parseHeaders(data []byte) ([]Header, string, error) {
	var hs []Header
	for len(data) > 0 {
		if data[0] == '\n' {
			return hs, string(data[1:]), nil
		}
		eol := bytes.IndexByte(data, '\n')
		if eol < 0 {
			return nil, "", fmt.Errorf("%w: unterminated header", ErrInvalid)
		}
		line := data[:eol]
		data = data[eol+1:]
		if bytes.IndexByte(line, 0) >= 0 {
			return nil, "", fmt.Errorf("%w: NUL in header", ErrInvalid)
		}
		if line[0] == ' ' {
			if len(hs) == 0 {
				return nil, "", fmt.Errorf("%w: continuation before first header", ErrInvalid)
			}
			hs[len(hs)-1].Value += "\n" + string(line[1:])
			continue
		}
		k, v, ok := bytes.Cut(line, []byte{' '})
		if !ok {
			return nil, "", fmt.Errorf("%w: header without value", ErrInvalid)
		}
		hs = append(hs, Header{Key: string(k), Value: string(v)})
	}
	return hs, "", nil
}
