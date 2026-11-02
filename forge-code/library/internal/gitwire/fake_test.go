package gitwire

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// fakeRemote is an in-memory smart HTTP server built on the server helpers.
// It serves one repository at /repo.git with git's CAS semantics.
type fakeRemote struct {
	srv *httptest.Server

	mu       sync.Mutex
	refs     map[string]gitobj.ID
	objects  map[gitobj.ID]gitobj.Object
	caps     []string
	v2caps   []string
	sideband bool // answer pushes on side-band when asked
	hook     func(w http.ResponseWriter, r *http.Request) bool
	headers  []http.Header
	lengths  []int64
	bodies   int
	lastCaps Capabilities
	lastArgs []string
	hadPack  bool
}

func newFakeRemote(t *testing.T) *fakeRemote {
	t.Helper()
	f := &fakeRemote{
		refs:     map[string]gitobj.ID{},
		objects:  map[gitobj.ID]gitobj.Object{},
		caps:     []string{"report-status", "delete-refs", "side-band-64k", "atomic", "object-format=sha1", "agent=fake"},
		v2caps:   []string{"agent=fake", "ls-refs", "fetch=shallow filter", "object-format=sha1"},
		sideband: true,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRemote) url() string { return f.srv.URL + "/repo.git" }

func (f *fakeRemote) add(objs ...gitobj.Object) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range objs {
		f.objects[o.ID()] = o
	}
}

func (f *fakeRemote) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.headers = append(f.headers, r.Header.Clone())
	f.lengths = append(f.lengths, r.ContentLength)
	hook := f.hook
	f.mu.Unlock()
	if hook != nil && hook(w, r) {
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/repo.git/info/refs" && r.URL.RawQuery == "service=git-receive-pack":
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", receivePackAdvertisement)
		WriteAdvertisement(w, f.refs, f.caps)
	case r.Method == "GET" && r.URL.Path == "/repo.git/info/refs" && r.URL.RawQuery == "service=git-upload-pack":
		w.Header().Set("Content-Type", uploadPackAdvertisement)
		if r.Header.Get("Git-Protocol") != "version=2" {
			http.Error(w, "v2 only", http.StatusBadRequest)
			return
		}
		WriteV2Advertisement(w, f.v2caps)
	case r.Method == "POST" && r.URL.Path == "/repo.git/git-receive-pack":
		f.receive(w, r)
	case r.Method == "POST" && r.URL.Path == "/repo.git/git-upload-pack":
		f.upload(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeRemote) receive(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != receivePackRequest {
		http.Error(w, "bad type", http.StatusUnsupportedMediaType)
		return
	}
	cmds, caps, rest, err := ParseCommands(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies++
	f.lastCaps = caps
	rep := &Report{UnpackOK: true, Refs: map[string]RefStatus{}}
	deletesOnly := !slices.ContainsFunc(cmds, func(c Command) bool { return !c.New.IsZero() })
	f.hadPack = !deletesOnly
	if !deletesOnly {
		objs, err := ReadPack(rest, 1<<24)
		if err != nil {
			rep.UnpackOK, rep.Unpack = false, err.Error()
		}
		for _, o := range objs {
			f.objects[o.ID()] = o
		}
	} else {
		var b [1]byte
		if n, _ := rest.Read(b[:]); n > 0 {
			f.hadPack = true
		}
	}
	ok := rep.UnpackOK
	for _, c := range cmds {
		reason := ""
		switch {
		case !rep.UnpackOK:
			reason = "unpacker error"
		case f.refs[c.Name] != c.Old:
			reason = "stale info"
		case !c.New.IsZero() && f.objects[c.New].Type == "":
			reason = "missing necessary objects"
		case strings.HasPrefix(c.Name, "refs/heads/protected"):
			reason = "pre-receive hook declined"
		}
		if reason != "" {
			ok = false
			rep.Refs[c.Name] = RefStatus{Reason: reason}
		}
	}
	atomic := caps.Has("atomic")
	for _, c := range cmds {
		if _, failed := rep.Refs[c.Name]; failed {
			continue
		}
		if atomic && !ok {
			rep.Refs[c.Name] = RefStatus{Reason: "atomic transaction failed"}
			continue
		}
		if c.New.IsZero() {
			delete(f.refs, c.Name)
		} else {
			f.refs[c.Name] = c.New
		}
		rep.Refs[c.Name] = RefStatus{OK: true}
	}
	w.Header().Set("Content-Type", receivePackResult)
	if caps.Has("report-status") {
		WriteReport(w, rep, f.sideband && caps.Has("side-band-64k"))
	}
}

func (f *fakeRemote) upload(w http.ResponseWriter, r *http.Request) {
	req, err := ParseV2Request(r.Body)
	if err != nil || req.Command != "fetch" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCaps, f.lastArgs = req.Caps, req.Args
	w.Header().Set("Content-Type", uploadPackResult)
	var objs []gitobj.Object
	var shallow []gitobj.ID
	for _, a := range req.Args {
		if hex, ok := strings.CutPrefix(a, "want "); ok {
			id, err := gitobj.ParseID(hex)
			o, found := f.objects[id]
			if err != nil || !found {
				WriteError(w, "upload-pack: not our ref "+hex)
				return
			}
			objs = append(objs, o)
			shallow = append(shallow, id)
		}
	}
	WriteFetchResponse(w, shallow, objs)
}
