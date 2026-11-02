package gitobj

import (
	"bytes"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mustID(t *testing.T, s string) ID {
	t.Helper()
	id, err := ParseID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHashKnownIDs(t *testing.T) {
	for _, tc := range []struct {
		typ  Type
		data string
		want string
	}{
		{TypeBlob, "", "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"},
		{TypeBlob, "hello\n", "ce013625030ba8dba906f756967f9e9ca394464a"},
		{TypeTree, "", "4b825dc642cb6eb9a060e54bf8d69288fbee4904"},
	} {
		if got := Hash(tc.typ, []byte(tc.data)).String(); got != tc.want {
			t.Errorf("Hash(%s, %q) = %s, want %s", tc.typ, tc.data, got, tc.want)
		}
		if got := (Object{tc.typ, []byte(tc.data)}).ID().String(); got != tc.want {
			t.Errorf("Object.ID = %s, want %s", got, tc.want)
		}
	}
}

func TestParseID(t *testing.T) {
	ok := "ce013625030ba8dba906f756967f9e9ca394464a"
	if id := mustID(t, ok); id.String() != ok || id.IsZero() {
		t.Fatalf("round trip: %s", id)
	}
	if !ZeroID.IsZero() || ZeroID.String() != strings.Repeat("0", 40) {
		t.Fatal("ZeroID")
	}
	for _, bad := range []string{"", "ce01", strings.ToUpper(ok), ok + "0", ok[:39] + "g", strings.Repeat("0", 64)} {
		if _, err := ParseID(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseID(%q) err = %v", bad, err)
		}
	}
}

func TestModeString(t *testing.T) {
	for m, want := range map[Mode]string{ModeTree: "40000", ModeFile: "100644", ModeExec: "100755", ModeSymlink: "120000", ModeSubmodule: "160000"} {
		if m.String() != want || !m.Valid() {
			t.Errorf("%o: %q", uint32(m), m.String())
		}
	}
	if Mode(0o100664).Valid() {
		t.Error("100664 valid")
	}
	if ModeTree.Type() != TypeTree || ModeSubmodule.Type() != TypeCommit || ModeSymlink.Type() != TypeBlob {
		t.Error("Mode.Type")
	}
}

func TestCheckName(t *testing.T) {
	for _, good := range []string{"a", "README.md", ".github", ".gitignore", "git", ".gitx", "x.git", "ünï", "a b", `a\b`} {
		if err := CheckName(good); err != nil {
			t.Errorf("CheckName(%q) = %v", good, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", "/", "a\x00", ".git", ".GIT", ".Git", ".git.", ".git ", ".git. .", "git~1", "GIT~1",
		".g\u200cit", "\ufeff.git", `a\.git`, `.git\a`, ".git:stream"} {
		if err := CheckName(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("CheckName(%q) = %v", bad, err)
		}
	}
}

func TestSortEntries(t *testing.T) {
	e := []TreeEntry{
		{Name: "foo", Mode: ModeTree},
		{Name: "foo.c", Mode: ModeFile},
		{Name: "foo-bar", Mode: ModeFile},
		{Name: "fo", Mode: ModeFile},
		{Name: "sub", Mode: ModeSubmodule},
		{Name: "sub.x", Mode: ModeFile},
		{Name: "B", Mode: ModeFile},
	}
	SortEntries(e)
	var got []string
	for _, x := range e {
		got = append(got, x.Name)
	}
	// Trees sort as "foo/", after "foo.c"; a submodule sorts as a file.
	want := []string{"B", "fo", "foo-bar", "foo.c", "foo", "sub", "sub.x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestEncodeTree(t *testing.T) {
	blob := Hash(TypeBlob, []byte("hello\n"))
	in := []TreeEntry{
		{Name: "z", Mode: ModeFile, ID: blob},
		{Name: "a", Mode: ModeTree, ID: Hash(TypeTree, nil)},
	}
	data, err := EncodeTree(in)
	if err != nil {
		t.Fatal(err)
	}
	if in[0].Name != "z" {
		t.Fatal("EncodeTree changed its input")
	}
	want := "40000 a\x00" + string(in[1].ID[:]) + "100644 z\x00" + string(blob[:])
	if string(data) != want {
		t.Fatalf("encoding %q", data)
	}
	got, err := DecodeTree(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []TreeEntry{in[1], in[0]}) {
		t.Fatalf("decode %+v", got)
	}
	if data, err := EncodeTree(nil); err != nil || len(data) != 0 {
		t.Fatalf("empty tree: %q %v", data, err)
	}

	for name, bad := range map[string][]TreeEntry{
		"dup":        {{Name: "a", Mode: ModeFile}, {Name: "a", Mode: ModeTree}},
		"mode":       {{Name: "a", Mode: 0o100664}},
		"name":       {{Name: ".git", Mode: ModeTree}},
		"slash":      {{Name: "a/b", Mode: ModeFile}},
		"gitmodules": {{Name: ".gitmodules", Mode: ModeSymlink}},
	} {
		if _, err := EncodeTree(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestDecodeTreeErrors(t *testing.T) {
	id := strings.Repeat("x", Size)
	for _, bad := range []string{
		"100644 a\x00" + id[:5],
		"100644a\x00" + id,
		" a\x00" + id,
		"10064x a\x00" + id,
		"100644 \x00" + id,
		"100644 a" + id,
		"12345678 a\x00" + id,
	} {
		if _, err := DecodeTree([]byte(bad)); !errors.Is(err, ErrInvalid) {
			t.Errorf("DecodeTree(%q) err = %v", bad, err)
		}
	}
	// Legacy zero-padded modes still decode.
	got, err := DecodeTree([]byte("040000 d\x00" + id))
	if err != nil || got[0].Mode != ModeTree {
		t.Fatalf("legacy mode: %+v %v", got, err)
	}
}

func TestSignature(t *testing.T) {
	when := time.Date(2024, 3, 1, 12, 0, 0, 0, time.FixedZone("IST", 5*3600+30*60))
	s := Signature{Name: "Ada L", Email: "ada@example.com", When: when}
	enc, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if want := "Ada L <ada@example.com> 1709274600 +0530"; enc != want {
		t.Fatalf("Encode = %q, want %q", enc, want)
	}
	got, err := ParseSignature(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != s.Name || got.Email != s.Email || !got.When.Equal(when) {
		t.Fatalf("parse %+v", got)
	}
	if again, _ := got.Encode(); again != enc {
		t.Fatalf("re-encode %q", again)
	}

	west := Signature{Name: "B", When: time.Unix(0, 0).In(time.FixedZone("", -(8*3600 + 15*60)))}
	if enc, _ := west.Encode(); enc != "B <> 0 -0815" {
		t.Fatalf("west = %q", enc)
	}

	for _, bad := range []Signature{
		{Name: "", When: when},
		{Name: "a<b", When: when},
		{Name: "a", Email: "x>y", When: when},
		{Name: "a\nb", When: when},
		{Name: "a"},
		{Name: "a", When: time.Unix(0, 0).In(time.FixedZone("", 100*3600))},
	} {
		if _, err := bad.Encode(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Encode(%+v) err = %v", bad, err)
		}
	}
	for _, bad := range []string{"a b 1 +0000", "a <b 1 +0000", "a <b> +0000", "a <b> 1", "a <b> x +0000", "a <b> 1 0000", "a <b> 1 +0099", "a <b> -1 +0000"} {
		if _, err := ParseSignature(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseSignature(%q) err = %v", bad, err)
		}
	}
}

func sampleCommit(t *testing.T) Commit {
	t.Helper()
	when := time.Unix(1700000000, 0).In(time.FixedZone("", -5*3600))
	return Commit{
		Tree:      Hash(TypeTree, nil),
		Parents:   []ID{Hash(TypeBlob, []byte("p1")), Hash(TypeBlob, []byte("p2"))},
		Author:    Signature{Name: "A U Thor", Email: "a@example.com", When: when},
		Committer: Signature{Name: "C O Mitter", Email: "c@example.com", When: when.Add(time.Hour).UTC()},
		Extra:     []Header{{Key: "gpgsig", Value: "-----BEGIN-----\nabc\n\n-----END-----"}},
		Message:   "Subject\n\nBody\n",
	}
}

func TestCommitRoundTrip(t *testing.T) {
	c := sampleCommit(t)
	data, err := EncodeCommit(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nparent ") ||
		!strings.Contains(string(data), "\ncommitter C O Mitter <c@example.com> 1700003600 +0000\ngpgsig -----BEGIN-----\n abc\n \n -----END-----\n\nSubject") {
		t.Fatalf("encoding:\n%s", data)
	}
	got, err := DecodeCommit(data)
	if err != nil {
		t.Fatal(err)
	}
	again, err := EncodeCommit(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, data) {
		t.Fatalf("re-encode differs:\n%s\n---\n%s", again, data)
	}
	if got.Message != c.Message || len(got.Parents) != 2 || got.Extra[0] != c.Extra[0] {
		t.Fatalf("decode %+v", got)
	}

	// A root commit with an empty message.
	root := Commit{Tree: c.Tree, Author: c.Author, Committer: c.Author}
	data, err = EncodeCommit(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeCommit(data); err != nil || got.Parents != nil || got.Extra != nil || got.Message != "" {
		t.Fatalf("root: %+v %v", got, err)
	}
}

func TestCommitErrors(t *testing.T) {
	c := sampleCommit(t)
	for name, mod := range map[string]func(*Commit){
		"author":   func(c *Commit) { c.Author.Name = "" },
		"reserved": func(c *Commit) { c.Extra = []Header{{Key: "parent", Value: "x"}} },
		"key":      func(c *Commit) { c.Extra = []Header{{Key: "a b", Value: "x"}} },
		"nul":      func(c *Commit) { c.Extra = []Header{{Key: "a", Value: "\x00"}} },
		"message":  func(c *Commit) { c.Message = "a\x00b" },
	} {
		x := c
		mod(&x)
		if _, err := EncodeCommit(x); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	tree := "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n"
	sig := "a <b> 1 +0000\n"
	for _, bad := range []string{
		"",
		"parent 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n",
		tree + "committer " + sig + "author " + sig + "\n",
		tree + "author " + sig,
		tree + "author " + sig + "committer " + sig + "tree x\n\n",
		tree + "author " + sig + "committer " + sig + "x",
		" x\n" + tree,
		tree + "nospace\n",
		"tree 4B825DC642CB6EB9A060E54BF8D69288FBEE4904\nauthor " + sig + "committer " + sig + "\n",
		tree + "author a\x00 <b> 1 +0000\ncommitter " + sig + "\n",
	} {
		if _, err := DecodeCommit([]byte(bad)); !errors.Is(err, ErrInvalid) {
			t.Errorf("DecodeCommit(%q) err = %v", bad, err)
		}
	}
}

func TestTagRoundTrip(t *testing.T) {
	sig := Signature{Name: "T", Email: "t@example.com", When: time.Unix(1, 0).UTC()}
	tag := Tag{Object: Hash(TypeTree, nil), Type: TypeTree, Name: "v1", Tagger: &sig, Message: "msg\n"}
	data, err := EncodeTag(tag)
	if err != nil {
		t.Fatal(err)
	}
	want := "object 4b825dc642cb6eb9a060e54bf8d69288fbee4904\ntype tree\ntag v1\ntagger T <t@example.com> 1 +0000\n\nmsg\n"
	if string(data) != want {
		t.Fatalf("encoding %q", data)
	}
	got, err := DecodeTag(data)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := EncodeTag(got); !bytes.Equal(again, data) {
		t.Fatalf("re-encode %q", again)
	}
	tag.Tagger = nil
	data, _ = EncodeTag(tag)
	if got, err := DecodeTag(data); err != nil || got.Tagger != nil || got.Name != "v1" {
		t.Fatalf("no tagger: %+v %v", got, err)
	}
	for _, bad := range []Tag{{Object: tag.Object, Type: "x", Name: "v"}, {Object: tag.Object, Type: TypeBlob}, {Object: tag.Object, Type: TypeBlob, Name: "a\nb"}} {
		if _, err := EncodeTag(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("EncodeTag(%+v) err = %v", bad, err)
		}
	}
	for _, bad := range []string{"object x\ntype tree\ntag v\n\n", "type tree\n\n", "object 4b825dc642cb6eb9a060e54bf8d69288fbee4904\ntype nope\ntag v\n\n"} {
		if _, err := DecodeTag([]byte(bad)); !errors.Is(err, ErrInvalid) {
			t.Errorf("DecodeTag(%q) err = %v", bad, err)
		}
	}
}

// TestAgainstGit checks that git computes the same IDs and accepts the
// objects under its own fsck rules.
func TestAgainstGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	hash := func(typ Type, data []byte) string {
		t.Helper()
		cmd := exec.Command("git", "hash-object", "-t", string(typ), "--stdin")
		cmd.Dir = t.TempDir()
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		cmd.Stdin = bytes.NewReader(data)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git hash-object -t %s: %v: %s", typ, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	blob := Hash(TypeBlob, []byte("x"))
	tree, err := EncodeTree([]TreeEntry{
		{Name: "foo", Mode: ModeTree, ID: Hash(TypeTree, nil)},
		{Name: "foo.c", Mode: ModeFile, ID: blob},
		{Name: "run", Mode: ModeExec, ID: blob},
		{Name: "link", Mode: ModeSymlink, ID: blob},
		{Name: "mod", Mode: ModeSubmodule, ID: blob},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hash(TypeTree, tree), Hash(TypeTree, tree).String(); got != want {
		t.Errorf("tree: git %s, ours %s", got, want)
	}
	c := sampleCommit(t)
	c.Extra = nil
	commit, err := EncodeCommit(c)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hash(TypeCommit, commit), Hash(TypeCommit, commit).String(); got != want {
		t.Errorf("commit: git %s, ours %s", got, want)
	}
	tag, err := EncodeTag(Tag{Object: Hash(TypeCommit, commit), Type: TypeCommit, Name: "v1", Tagger: &c.Author, Message: "m\n"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hash(TypeTag, tag), Hash(TypeTag, tag).String(); got != want {
		t.Errorf("tag: git %s, ours %s", got, want)
	}
}

func FuzzDecode(f *testing.F) {
	c, _ := EncodeCommit(Commit{Tree: Hash(TypeTree, nil), Author: Signature{Name: "a", When: time.Unix(1, 0)}, Committer: Signature{Name: "a", When: time.Unix(1, 0)}, Message: "m"})
	f.Add(c)
	tr, _ := EncodeTree([]TreeEntry{{Name: "a", Mode: ModeFile}})
	f.Add(tr)
	tg, _ := EncodeTag(Tag{Type: TypeBlob, Name: "v"})
	f.Add(tg)
	f.Fuzz(func(t *testing.T, data []byte) {
		if c, err := DecodeCommit(data); err == nil {
			if _, err := EncodeCommit(c); err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
		}
		if es, err := DecodeTree(data); err == nil {
			for _, e := range es {
				if e.Name == "" {
					t.Fatal("empty name decoded")
				}
			}
		}
		if tg, err := DecodeTag(data); err == nil && !tg.Type.Valid() {
			t.Fatal("invalid type decoded")
		}
	})
}
