package gitwire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

var pushCaps = []string{"report-status", "atomic", "delete-refs", "object-format=sha1"}

// testCommit builds a root commit with one file and returns its objects,
// commit last.
func testCommit(t *testing.T, content string, parents ...gitobj.ID) []gitobj.Object {
	t.Helper()
	blob := gitobj.Object{Type: gitobj.TypeBlob, Data: []byte(content)}
	tree, err := gitobj.EncodeTree([]gitobj.TreeEntry{{Name: "file.txt", Mode: gitobj.ModeFile, ID: blob.ID()}})
	if err != nil {
		t.Fatal(err)
	}
	treeObj := gitobj.Object{Type: gitobj.TypeTree, Data: tree}
	sig := gitobj.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1700000000, 0).In(time.FixedZone("", 3600))}
	commit, err := gitobj.EncodeCommit(gitobj.Commit{Tree: treeObj.ID(), Parents: parents, Author: sig, Committer: sig, Message: content + "\n"})
	if err != nil {
		t.Fatal(err)
	}
	return []gitobj.Object{blob, treeObj, {Type: gitobj.TypeCommit, Data: commit}}
}

func packOf(t *testing.T, objs []gitobj.Object) io.Reader {
	t.Helper()
	var b bytes.Buffer
	if err := WritePack(&b, objs); err != nil {
		t.Fatal(err)
	}
	return &b
}

func TestAdvertise(t *testing.T) {
	f := newFakeRemote(t)
	ctx := context.Background()
	a, err := Advertise(ctx, nil, f.url())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Refs) != 0 || !a.Caps.Has("atomic") || !a.Caps.Has("report-status") {
		t.Fatalf("empty repo: %+v", a)
	}
	if v, ok := a.Caps.Value("object-format"); !ok || v != "sha1" {
		t.Fatalf("object-format %q %v", v, ok)
	}
	if v, ok := a.Caps.Value("atomic"); !ok || v != "" {
		t.Fatalf("atomic value %q", v)
	}
	if a.Caps.Has("ofs") {
		t.Fatal("prefix matched")
	}
	h := f.headers[0]
	if h.Get("Cache-Control") != "no-cache" || h.Get("Git-Protocol") != "" {
		t.Fatalf("headers %v", h)
	}

	c1 := testCommit(t, "one")[2].ID()
	c2 := testCommit(t, "two")[2].ID()
	f.refs["refs/heads/main"] = c1
	f.refs["refs/tags/v1"] = c2
	a, err = Advertise(ctx, http.DefaultClient, f.url()+"/")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Refs) != 2 || a.Refs["refs/heads/main"] != c1 || a.Refs["refs/tags/v1"] != c2 || !a.Caps.Has("delete-refs") {
		t.Fatalf("refs: %+v", a)
	}
}

func TestAdvertiseErrors(t *testing.T) {
	f := newFakeRemote(t)
	ctx := context.Background()
	reply := func(status int, ctype, body string) {
		f.hook = func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Type", ctype)
			w.WriteHeader(status)
			io.WriteString(w, body)
			return true
		}
	}
	for _, tc := range []struct {
		status int
		ctype  string
		body   string
		want   error
	}{
		{401, "text/plain", "auth", forgehttp.ErrUnauthorized},
		{403, "text/plain", "no", forgehttp.ErrForbidden},
		{404, "text/plain", "Repository not found", forgehttp.ErrNotFound},
		{200, "text/html", "<html>login</html>", ErrProtocol},
		{200, receivePackAdvertisement, "001f# service=git-upload-pack\n0000", ErrProtocol},
		{200, receivePackAdvertisement, "", ErrProtocol},
		{200, receivePackAdvertisement, "001f# service=git-receive-pack\n0000", ErrProtocol},
		{200, receivePackAdvertisement, "001f# service=git-receive-pack\n0000000eERR denied0000", ErrRemote},
		{200, receivePackAdvertisement, "000eERR denied", ErrRemote},
		{200, receivePackAdvertisement, "001f# service=git-receive-pack\n00000009bad\n0000", ErrProtocol},
		{200, receivePackAdvertisement, "001f# service=git-receive-pack\n0000" +
			pkt(strings.Repeat("0", 64)+" capabilities^{}\x00report-status object-format=sha256\n") + "0000", ErrUnsupported},
	} {
		reply(tc.status, tc.ctype, tc.body)
		_, err := Advertise(ctx, nil, f.url())
		if !errors.Is(err, tc.want) {
			t.Errorf("%d %s %q: err = %v, want %v", tc.status, tc.ctype, tc.body, err, tc.want)
		}
	}
	for _, bad := range []string{"ftp://x/r.git", "https://u:p@x/r.git", "https://x/r.git?a=b", "https://x/r.git#f", "/r.git", "%"} {
		if _, err := Advertise(ctx, nil, bad); err == nil || strings.Contains(err.Error(), "p@") {
			t.Errorf("Advertise(%q) err = %v", bad, err)
		}
	}
}

func pkt(s string) string {
	var b bytes.Buffer
	NewPktWriter(&b).Data([]byte(s))
	return b.String()
}

func TestParseAdvertisement(t *testing.T) {
	id := strings.Repeat("a", 40)
	hdr := "001f# service=git-receive-pack\n0000"
	for name, tc := range map[string]struct {
		body string
		refs int
		caps int
		err  error
	}{
		"bare flush":   {hdr + "0000", 0, 0, nil},
		"version 1":    {hdr + pkt("version 1\n") + pkt(id+" refs/heads/a\x00atomic\n") + "0000", 1, 1, nil},
		"have":         {hdr + pkt(id+" refs/heads/a\x00atomic\n") + pkt(id+" .have\n") + pkt(id+" refs/tags/t^{}\n") + "0000", 1, 1, nil},
		"dup":          {hdr + pkt(id+" refs/heads/a\x00\n") + pkt(id+" refs/heads/a\n") + "0000", 0, 0, ErrProtocol},
		"late caps":    {hdr + pkt(id+" refs/heads/a\x00\n") + pkt(id+" refs/heads/b\x00x\n") + "0000", 0, 0, ErrProtocol},
		"bad id":       {hdr + pkt(strings.ToUpper(id)+" refs/heads/a\x00\n") + "0000", 0, 0, ErrProtocol},
		"no flush":     {hdr + pkt(id+" refs/heads/a\x00\n"), 0, 0, ErrProtocol},
		"caps nonzero": {hdr + pkt(id+" capabilities^{}\x00\n") + "0000", 0, 0, ErrProtocol},
		"delim":        {hdr + "0001", 0, 0, ErrProtocol},
		"header junk":  {"001f# service=git-receive-pack\n" + pkt("junk") + "0000" + "0000", 0, 0, nil},
		"header noend": {"001f# service=git-receive-pack\n", 0, 0, ErrProtocol},
	} {
		a, err := parseAdvertisement(strings.NewReader(tc.body))
		if !errors.Is(err, tc.err) || (err == nil && (len(a.Refs) != tc.refs || len(a.Caps) != tc.caps)) {
			t.Errorf("%s: %+v %v", name, a, err)
		}
	}
}

func TestPush(t *testing.T) {
	for _, sideband := range []bool{false, true} {
		f := newFakeRemote(t)
		ctx := context.Background()
		caps := pushCaps
		if sideband {
			caps = append(caps, "side-band-64k")
		}
		objs := testCommit(t, "one")
		c1 := objs[2].ID()
		rep, err := Push(ctx, nil, f.url(), []Command{{Name: "refs/heads/main", New: c1}}, caps, packOf(t, objs))
		if err != nil {
			t.Fatal(err)
		}
		if !rep.UnpackOK || !rep.Refs["refs/heads/main"].OK || f.refs["refs/heads/main"] != c1 {
			t.Fatalf("create: %+v", rep)
		}
		h := f.headers[len(f.headers)-1]
		if h.Get("Content-Type") != receivePackRequest || h.Get("Accept") != receivePackResult {
			t.Fatalf("headers %v", h)
		}
		if strings.Join(f.lastCaps, " ") != strings.Join(caps, " ") {
			t.Fatalf("caps sent %v", f.lastCaps)
		}
		if f.lengths[len(f.lengths)-1] <= 0 {
			t.Fatalf("no Content-Length for a buffered pack")
		}

		// A pack of unknown length is sent chunked.
		rep, err = Push(ctx, nil, f.url(), []Command{{Name: "refs/heads/chunked", New: c1}}, caps, io.MultiReader(packOf(t, objs)))
		if err != nil || !rep.Refs["refs/heads/chunked"].OK || f.lengths[len(f.lengths)-1] != -1 {
			t.Fatalf("chunked: %+v %v %v", rep, err, f.lengths)
		}

		// A move to an existing object with a nil pack sends an empty pack.
		rep, err = Push(ctx, nil, f.url(), []Command{{Name: "refs/heads/other", New: c1}}, caps, nil)
		if err != nil || !rep.Refs["refs/heads/other"].OK || !f.hadPack {
			t.Fatalf("move: %+v %v", rep, err)
		}

		// Stale old value: ng, ref unchanged.
		objs2 := testCommit(t, "two", c1)
		c2 := objs2[2].ID()
		rep, err = Push(ctx, nil, f.url(), []Command{{Name: "refs/heads/main", Old: c2, New: c2}}, caps, packOf(t, objs2))
		if err != nil {
			t.Fatal(err)
		}
		if st := rep.Refs["refs/heads/main"]; st.OK || st.Reason != "stale info" || f.refs["refs/heads/main"] != c1 {
			t.Fatalf("stale: %+v", rep)
		}

		// Atomic: one bad command fails both.
		rep, err = Push(ctx, nil, f.url(), []Command{
			{Name: "refs/heads/main", Old: c1, New: c2},
			{Name: "refs/heads/protected", New: c2},
		}, caps, nil)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Refs["refs/heads/main"].OK || rep.Refs["refs/heads/protected"].Reason != "pre-receive hook declined" || f.refs["refs/heads/main"] != c1 {
			t.Fatalf("atomic: %+v", rep)
		}

		// Delete without a pack.
		rep, err = Push(ctx, nil, f.url(), []Command{{Name: "refs/heads/other", Old: c1}}, caps, nil)
		if err != nil || !rep.Refs["refs/heads/other"].OK || f.hadPack {
			t.Fatalf("delete: %+v %v hadPack=%v", rep, err, f.hadPack)
		}
		if _, ok := f.refs["refs/heads/other"]; ok {
			t.Fatal("ref not deleted")
		}
	}
}

func TestPushInvalid(t *testing.T) {
	ctx := context.Background()
	id := testCommit(t, "x")[2].ID()
	ok := []Command{{Name: "refs/heads/a", New: id}}
	for name, tc := range map[string]struct {
		cmds []Command
		caps []string
	}{
		"no cmds":    {nil, pushCaps},
		"no report":  {ok, []string{"atomic"}},
		"v2 report":  {ok, append([]string{"report-status-v2"}, pushCaps...)},
		"bad cap":    {ok, append([]string{"a b"}, pushCaps...)},
		"empty cap":  {ok, append([]string{""}, pushCaps...)},
		"name":       {[]Command{{Name: "HEAD", New: id}}, pushCaps},
		"name space": {[]Command{{Name: "refs/heads/a b", New: id}}, pushCaps},
		"name nul":   {[]Command{{Name: "refs/heads/a\x00", New: id}}, pushCaps},
		"dup":        {[]Command{ok[0], ok[0]}, pushCaps},
		"zero":       {[]Command{{Name: "refs/heads/a"}}, pushCaps},
	} {
		if _, err := Push(ctx, nil, "http://127.0.0.1:1/r.git", tc.cmds, tc.caps, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestPushResponseErrors(t *testing.T) {
	f := newFakeRemote(t)
	ctx := context.Background()
	objs := testCommit(t, "x")
	cmds := []Command{{Name: "refs/heads/a", New: objs[2].ID()}}
	for _, tc := range []struct {
		status int
		ctype  string
		body   string
		want   error
	}{
		{403, "text/plain", "denied", forgehttp.ErrForbidden},
		{200, "text/plain", "", ErrProtocol},
		{200, receivePackResult, "", ErrProtocol},
		{200, receivePackResult, pkt("\x01" + pkt("unpack ok\n")), ErrProtocol},
		{200, receivePackResult, pkt("\x03hook exploded\n") + "0000", ErrRemote},
		{200, receivePackResult, pkt("\x01"+pkt("unpack ok\n")+pkt("ok refs/heads/a\n")+"0000") + "0000", nil},
	} {
		f.hook = func(w http.ResponseWriter, r *http.Request) bool {
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", tc.ctype)
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
			return true
		}
		_, err := Push(ctx, nil, f.url(), cmds, append(pushCaps, "side-band-64k"), packOf(t, objs))
		if !errors.Is(err, tc.want) {
			t.Errorf("%d %q: err = %v, want %v", tc.status, tc.body, err, tc.want)
		}
	}
}

func TestParseReport(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want *Report
		err  error
	}{
		"ok":         {pkt("unpack ok\n") + pkt("ok refs/heads/a\n") + "0000", &Report{UnpackOK: true, Refs: map[string]RefStatus{"refs/heads/a": {OK: true}}}, nil},
		"ng":         {pkt("unpack ok\n") + pkt("ng refs/heads/a non-fast-forward\n") + pkt("ng refs/heads/b\n") + "0000", &Report{UnpackOK: true, Refs: map[string]RefStatus{"refs/heads/a": {Reason: "non-fast-forward"}, "refs/heads/b": {Reason: "rejected"}}}, nil},
		"unpack":     {pkt("unpack index-pack failed\n") + pkt("ng refs/heads/a unpacker error\n") + "0000", &Report{Unpack: "index-pack failed", Refs: map[string]RefStatus{"refs/heads/a": {Reason: "unpacker error"}}}, nil},
		"empty":      {"0000", nil, ErrProtocol},
		"no unpack":  {pkt("ok refs/heads/a\n") + "0000", nil, ErrProtocol},
		"bad line":   {pkt("unpack ok\n") + pkt("maybe refs/heads/a\n") + "0000", nil, ErrProtocol},
		"empty name": {pkt("unpack ok\n") + pkt("ok \n") + "0000", nil, ErrProtocol},
		"dup":        {pkt("unpack ok\n") + pkt("ok refs/heads/a\n") + pkt("ng refs/heads/a x\n") + "0000", nil, ErrProtocol},
		"truncated":  {pkt("unpack ok\n") + pkt("ok refs/heads/a\n"), nil, ErrProtocol},
		"delim":      {pkt("unpack ok\n") + "0001", nil, ErrProtocol},
		"ERR":        {pkt("ERR denied\n"), nil, ErrRemote},
	} {
		got, err := parseReport(strings.NewReader(tc.body))
		if !errors.Is(err, tc.err) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.err)
			continue
		}
		if tc.want != nil && (got.UnpackOK != tc.want.UnpackOK || got.Unpack != tc.want.Unpack || len(got.Refs) != len(tc.want.Refs)) {
			t.Errorf("%s: %+v", name, got)
		}
		for n, st := range tc.want.refs() {
			if got.Refs[n] != st {
				t.Errorf("%s: %s = %+v, want %+v", name, n, got.Refs[n], st)
			}
		}
	}
}

func (r *Report) refs() map[string]RefStatus {
	if r == nil {
		return nil
	}
	return r.Refs
}

func TestWriteReportRoundTrip(t *testing.T) {
	rep := &Report{UnpackOK: true, Refs: map[string]RefStatus{"refs/heads/b": {Reason: "x y"}, "refs/heads/a": {OK: true}}}
	for _, sb := range []bool{false, true} {
		var b bytes.Buffer
		if err := WriteReport(&b, rep, sb); err != nil {
			t.Fatal(err)
		}
		got, err := readReport(&b, sb)
		if err != nil || !got.UnpackOK || got.Refs["refs/heads/b"].Reason != "x y" || !got.Refs["refs/heads/a"].OK {
			t.Fatalf("sideband=%v: %+v %v", sb, got, err)
		}
	}
	var b bytes.Buffer
	WriteReport(&b, &Report{}, false)
	if got, err := readReport(&b, false); err != nil || got.UnpackOK || got.Unpack != "error" {
		t.Fatalf("unpack error: %+v %v", got, err)
	}
}

func TestParseCommands(t *testing.T) {
	var b bytes.Buffer
	a, c := strings.Repeat("a", 40), strings.Repeat("c", 40)
	b.WriteString(pkt(a + " " + c + " refs/heads/x\x00report-status atomic\n"))
	b.WriteString(pkt(c + " " + strings.Repeat("0", 40) + " refs/heads/y\n"))
	b.WriteString("0000PACKrest")
	cmds, caps, rest, err := ParseCommands(&b)
	if err != nil || len(cmds) != 2 || cmds[1].Name != "refs/heads/y" || !cmds[1].New.IsZero() || !caps.Has("atomic") {
		t.Fatalf("%+v %v %v", cmds, caps, err)
	}
	if r, _ := io.ReadAll(rest); string(r) != "PACKrest" {
		t.Fatalf("rest %q", r)
	}
	for _, bad := range []string{
		pkt(a+" "+c+"\n") + "0000",
		pkt(a+" "+c+" HEAD\n") + "0000",
		pkt("x "+c+" refs/heads/x\n") + "0000",
		pkt(a + " " + c + " refs/heads/x\n"),
		"0001",
	} {
		if _, _, _, err := ParseCommands(strings.NewReader(bad)); !errors.Is(err, ErrProtocol) {
			t.Errorf("ParseCommands(%q) err = %v", bad, err)
		}
	}
}

func TestFetchCommit(t *testing.T) {
	f := newFakeRemote(t)
	ctx := context.Background()
	objs := testCommit(t, "one")
	f.add(objs...)
	want := objs[2]
	got, err := FetchCommit(ctx, nil, f.url(), want.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want.Data) {
		t.Fatalf("commit %q", got)
	}
	wantArgs := []string{"no-progress", "want " + want.ID().String(), "deepen 1", "filter tree:0", "done"}
	if strings.Join(f.lastArgs, "|") != strings.Join(wantArgs, "|") || strings.Join(f.lastCaps, "|") != "object-format=sha1" {
		t.Fatalf("request caps %v args %v", f.lastCaps, f.lastArgs)
	}
	for _, h := range f.headers {
		if h.Get("Git-Protocol") != "version=2" {
			t.Fatalf("missing Git-Protocol: %v", h)
		}
	}
	if h := f.headers[1]; h.Get("Content-Type") != uploadPackRequest || h.Get("Accept") != uploadPackResult {
		t.Fatalf("POST headers %v", h)
	}

	// Unknown object: the server says ERR.
	if _, err := FetchCommit(ctx, nil, f.url(), testCommit(t, "nope")[2].ID()); !errors.Is(err, ErrRemote) || !strings.Contains(err.Error(), "not our ref") {
		t.Fatalf("unknown: %v", err)
	}
	// The pack must hold the commit we asked for.
	if _, err := FetchCommit(ctx, nil, f.url(), objs[0].ID()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("blob: %v", err)
	}
}

func TestFetchCommitErrors(t *testing.T) {
	ctx := context.Background()
	objs := testCommit(t, "one")
	for name, tc := range map[string]struct {
		caps []string
		hook func(w http.ResponseWriter, r *http.Request) bool
		want error
	}{
		"no filter":  {caps: []string{"fetch=shallow"}, want: ErrUnsupported},
		"no shallow": {caps: []string{"fetch=filter"}, want: ErrUnsupported},
		"no fetch":   {caps: []string{"ls-refs"}, want: ErrUnsupported},
		"sha256":     {caps: []string{"fetch=shallow filter", "object-format=sha256"}, want: ErrUnsupported},
		"v0": {hook: func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Type", uploadPackAdvertisement)
			io.WriteString(w, "001e# service=git-upload-pack\n0000"+pkt(strings.Repeat("a", 40)+" HEAD\x00multi_ack\n")+"0000")
			return true
		}, want: ErrUnsupported},
		"401": {hook: func(w http.ResponseWriter, r *http.Request) bool {
			http.Error(w, "auth", http.StatusUnauthorized)
			return true
		}, want: forgehttp.ErrUnauthorized},
		"no packfile": {hook: func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method == "GET" {
				return false
			}
			w.Header().Set("Content-Type", uploadPackResult)
			io.WriteString(w, pkt("shallow-info\n")+pkt("shallow "+strings.Repeat("a", 40)+"\n")+"0000")
			return true
		}, want: ErrProtocol},
		"odd section": {hook: func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method == "GET" {
				return false
			}
			w.Header().Set("Content-Type", uploadPackResult)
			io.WriteString(w, pkt("surprise\n")+"0001")
			return true
		}, want: ErrProtocol},
		"band 3": {hook: func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method == "GET" {
				return false
			}
			w.Header().Set("Content-Type", uploadPackResult)
			io.WriteString(w, pkt("packfile\n")+pkt("\x03pack-objects died")+"0000")
			return true
		}, want: ErrRemote},
	} {
		f := newFakeRemote(t)
		f.add(objs...)
		if tc.caps != nil {
			f.v2caps = tc.caps
		}
		f.hook = tc.hook
		if _, err := FetchCommit(ctx, nil, f.url(), objs[2].ID()); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestParseV2Advertisement(t *testing.T) {
	caps, err := parseV2Advertisement(strings.NewReader("001e# service=git-upload-pack\n0000" + pkt("version 2\n") + pkt("fetch=shallow filter\n") + "0000"))
	if err != nil || len(caps) != 1 || !caps.Has("fetch") {
		t.Fatalf("%v %v", caps, err)
	}
	for _, bad := range []string{"", pkt("version 2\n"), pkt("version 2\n") + "0001", pkt("version 2\n") + "0004" + "0000", pkt("ERR nope\n")} {
		if _, err := parseV2Advertisement(strings.NewReader(bad)); err == nil {
			t.Errorf("parseV2Advertisement(%q) ok", bad)
		}
	}
}

func TestParseV2Request(t *testing.T) {
	body := pkt("command=fetch\n") + pkt("agent=x\n") + "0001" + pkt("want a\n") + pkt("done\n") + "0000"
	req, err := ParseV2Request(strings.NewReader(body))
	if err != nil || req.Command != "fetch" || len(req.Caps) != 1 || strings.Join(req.Args, ",") != "want a,done" {
		t.Fatalf("%+v %v", req, err)
	}
	for _, bad := range []string{"0000", pkt("agent=x\n") + "0000", pkt("command=fetch\n") + "00010001", pkt("command=fetch\n"), pkt("command=fetch\n") + "0002"} {
		if _, err := ParseV2Request(strings.NewReader(bad)); !errors.Is(err, ErrProtocol) {
			t.Errorf("ParseV2Request(%q) err = %v", bad, err)
		}
	}
	if WriteError(io.Discard, "a\nb") == nil || WriteError(io.Discard, "") == nil {
		t.Fatal("WriteError accepted a bad message")
	}
}
