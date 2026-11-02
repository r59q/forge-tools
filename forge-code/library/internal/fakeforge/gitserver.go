package fakeforge

import (
	"bytes"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitwire"
)

// maxPushBytes caps a receive-pack request body.
const maxPushBytes = 256 << 20

// GitOptions tune the Git front.
type GitOptions struct {
	// NoAtomic leaves out the atomic capability, so each command of a
	// push applies on its own.
	NoAtomic bool
	// NoDeleteRefs leaves out delete-refs; deletes then get ng.
	NoDeleteRefs bool
	// ObjectFormat is the advertised object format. Empty means "sha1".
	ObjectFormat string
	// NoFilter leaves shallow and filter out of the v2 fetch capability.
	NoFilter bool
	// Decline plays a pre-receive hook: if it returns true, every command
	// gets ng "pre-receive hook declined" and nothing changes.
	Decline func(cmds []gitwire.Command) bool
	// BeforeReceive runs for each push after its commands are read and
	// before they are applied, so it sits between the advertisement and
	// the update. Tests use it to move refs.
	BeforeReceive func(r *Repo, cmds []gitwire.Command)
	// AbortBeforeApply drops the connection after reading a push,
	// without applying it.
	AbortBeforeApply bool
	// AbortAfterApply applies a push, then drops the connection without
	// a response.
	AbortAfterApply bool
}

// Git is the Git front with default options.
func Git(f *Forge) http.Handler { return GitFront(GitOptions{})(f) }

// GitFront returns a Git smart-HTTP front for the Forge's repositories,
// mounted under GitPrefix, at "/{repo}.git/{endpoint}":
//
//   - GET info/refs?service=git-receive-pack and POST git-receive-pack
//     need write access. Pushes apply under the repository lock with
//     git's rules: each Old must match (zero means absent), atomic is
//     all or nothing, deletes need delete-refs, branches must point to
//     commits, new objects must be connected, and pushed trees must be
//     canonical. Results use report-status, on side-band-64k if asked.
//   - GET info/refs?service=git-upload-pack (protocol v2 only) and POST
//     git-upload-pack serve one commit for a fetch with "deepen 1" and
//     "filter tree:0". The commit must be reachable from a ref.
//
// Auth is the Forge's: Basic auth with the token as password.
func GitFront(opts GitOptions) Front {
	if opts.ObjectFormat == "" {
		opts.ObjectFormat = "sha1"
	}
	return func(f *Forge) http.Handler {
		g := &gitFront{f: f, opts: opts}
		return http.HandlerFunc(g.serve)
	}
}

type gitFront struct {
	f    *Forge
	opts GitOptions
}

func (g *gitFront) serve(w http.ResponseWriter, r *http.Request) {
	repoPath, endpoint, ok := splitGitPath(r.URL.Path)
	if !ok {
		WriteMessage(w, http.StatusNotFound, "Not Found")
		return
	}
	service := r.URL.Query().Get("service")
	switch {
	case r.Method == http.MethodGet && endpoint == "info/refs" && service == "git-receive-pack":
		if repo := g.authorize(w, repoPath, true); repo != nil {
			g.advertise(w, repo)
		}
	case r.Method == http.MethodGet && endpoint == "info/refs" && service == "git-upload-pack":
		if !strings.Contains(r.Header.Get("Git-Protocol"), "version=2") {
			WriteMessage(w, http.StatusBadRequest, "protocol v2 only")
			return
		}
		if repo := g.authorize(w, repoPath, false); repo != nil {
			g.advertiseV2(w)
		}
	case r.Method == http.MethodPost && endpoint == "git-receive-pack":
		if r.Header.Get("Content-Type") != "application/x-git-receive-pack-request" {
			WriteMessage(w, http.StatusUnsupportedMediaType, "bad content type")
			return
		}
		if repo := g.authorize(w, repoPath, true); repo != nil {
			g.receive(w, r, repo)
		}
	case r.Method == http.MethodPost && endpoint == "git-upload-pack":
		if r.Header.Get("Content-Type") != "application/x-git-upload-pack-request" {
			WriteMessage(w, http.StatusUnsupportedMediaType, "bad content type")
			return
		}
		if repo := g.authorize(w, repoPath, false); repo != nil {
			g.upload(w, r, repo)
		}
	default:
		WriteMessage(w, http.StatusNotFound, "Not Found")
	}
}

// splitGitPath splits "/{repo}.git/{endpoint}".
func splitGitPath(p string) (repo, endpoint string, ok bool) {
	for _, ep := range []string{"info/refs", "git-receive-pack", "git-upload-pack"} {
		rest, found := strings.CutSuffix(p, "/"+ep)
		if !found {
			continue
		}
		repo, ok = strings.CutSuffix(strings.TrimPrefix(rest, "/"), ".git")
		return repo, ep, ok && repo != ""
	}
	return "", "", false
}

func (g *gitFront) authorize(w http.ResponseWriter, path string, write bool) *Repo {
	repo, status := g.f.Authorize(path, write)
	if status != 0 {
		WriteMessage(w, status, http.StatusText(status))
		return nil
	}
	return repo
}

// caps returns the receive-pack capabilities.
func (g *gitFront) caps() []string {
	caps := []string{"report-status", "side-band-64k", "quiet", "ofs-delta"}
	if !g.opts.NoDeleteRefs {
		caps = append(caps, "delete-refs")
	}
	if !g.opts.NoAtomic {
		caps = append(caps, "atomic")
	}
	return append(caps, "object-format="+g.opts.ObjectFormat, "agent=fakeforge")
}

func (g *gitFront) advertise(w http.ResponseWriter, repo *Repo) {
	repo.mu.RLock()
	refs := make(map[string]gitobj.ID, len(repo.refs))
	for name, id := range repo.refs {
		refs[name] = id
	}
	repo.mu.RUnlock()
	w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	_ = gitwire.WriteAdvertisement(w, refs, g.caps())
}

func (g *gitFront) advertiseV2(w http.ResponseWriter) {
	fetch := "fetch=shallow filter"
	if g.opts.NoFilter {
		fetch = "fetch=shallow"
	}
	w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	_ = gitwire.WriteV2Advertisement(w, []string{"agent=fakeforge", "ls-refs", fetch, "object-format=" + g.opts.ObjectFormat})
}

// receive serves one push.
func (g *gitFront) receive(w http.ResponseWriter, r *http.Request, repo *Repo) {
	cmds, caps, rest, err := gitwire.ParseCommands(http.MaxBytesReader(w, r.Body, maxPushBytes))
	if err != nil {
		WriteMessage(w, http.StatusBadRequest, err.Error())
		return
	}
	// A pack comes unless every command is a delete.
	var objs []gitobj.Object
	unpackErr := ""
	if slices.ContainsFunc(cmds, func(c gitwire.Command) bool { return !c.New.IsZero() }) {
		objs, unpackErr = unpack(rest)
	}
	if g.opts.AbortBeforeApply {
		panic(http.ErrAbortHandler)
	}
	if g.opts.BeforeReceive != nil {
		g.opts.BeforeReceive(repo, cmds)
	}
	rep := g.apply(repo, cmds, caps, objs, unpackErr)
	if g.opts.AbortAfterApply {
		panic(http.ErrAbortHandler)
	}
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	_ = gitwire.WriteReport(w, rep, caps.Has("side-band-64k"))
}

// unpack reads a pack and checks its objects as receive.fsckObjects
// does: each must parse, and trees must be canonical.
func unpack(r io.Reader) ([]gitobj.Object, string) {
	objs, err := gitwire.ReadPack(r, maxPushBytes)
	if err != nil {
		return nil, err.Error()
	}
	for _, o := range objs {
		if err := checkObject(o); err != nil {
			return nil, "fsck error: " + err.Error()
		}
		if o.Type == gitobj.TypeTree {
			entries, _ := gitobj.DecodeTree(o.Data)
			data, err := gitobj.EncodeTree(entries)
			if err != nil || !bytes.Equal(data, o.Data) {
				return nil, "fsck error: tree " + o.ID().String() + " is not canonical"
			}
		}
	}
	return objs, ""
}

// apply checks and applies cmds under the repository lock and returns
// the report.
func (g *gitFront) apply(repo *Repo, cmds []gitwire.Command, caps gitwire.Capabilities, objs []gitobj.Object, unpackErr string) *gitwire.Report {
	rep := &gitwire.Report{UnpackOK: unpackErr == "", Unpack: unpackErr, Refs: map[string]gitwire.RefStatus{}}
	fail := func(reason string) *gitwire.Report {
		for _, c := range cmds {
			rep.Refs[c.Name] = gitwire.RefStatus{Reason: reason}
		}
		return rep
	}
	if unpackErr != "" {
		return fail("unpacker error")
	}
	if g.opts.Decline != nil && g.opts.Decline(cmds) {
		return fail("pre-receive hook declined")
	}
	quarantine := make(map[gitobj.ID]gitobj.Object, len(objs))
	for _, o := range objs {
		quarantine[o.ID()] = o
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	reasons := make([]string, len(cmds))
	for i, c := range cmds {
		reasons[i] = repo.check(c, quarantine, g.opts.NoDeleteRefs)
	}
	atomic := !g.opts.NoAtomic && caps.Has("atomic")
	if atomic && slices.ContainsFunc(reasons, func(s string) bool { return s != "" }) {
		for i, c := range cmds {
			if reasons[i] == "" {
				reasons[i] = "atomic push failure"
			}
			rep.Refs[c.Name] = gitwire.RefStatus{Reason: reasons[i]}
		}
		return rep
	}
	applied := false
	for i, c := range cmds {
		if reasons[i] != "" {
			rep.Refs[c.Name] = gitwire.RefStatus{Reason: reasons[i]}
			continue
		}
		if c.New.IsZero() {
			delete(repo.refs, c.Name)
		} else {
			repo.refs[c.Name] = c.New
		}
		applied = true
		rep.Refs[c.Name] = gitwire.RefStatus{OK: true}
	}
	if applied {
		for id, o := range quarantine {
			if _, ok := repo.objects[id]; !ok {
				repo.objects[id] = o
			}
		}
	}
	return rep
}

// check returns why c cannot apply, or "". The caller holds r.mu.
func (r *Repo) check(c gitwire.Command, quarantine map[gitobj.ID]gitobj.Object, noDeletes bool) string {
	if c.New.IsZero() {
		if noDeletes {
			return "deletion prohibited"
		}
	} else {
		o, ok := quarantine[c.New]
		if !ok {
			o, ok = r.objects[c.New]
		}
		if !ok || !r.connected(c.New, quarantine) {
			return "missing necessary objects"
		}
		if strings.HasPrefix(c.Name, "refs/heads/") && o.Type != gitobj.TypeCommit {
			return "non-commit object for a branch"
		}
	}
	if r.refs[c.Name] != c.Old {
		return "failed to update ref"
	}
	return ""
}

// connected reports whether every object reachable from id is stored or
// in quarantine. Stored objects count as complete, as git's connectivity
// check stops at existing refs. The caller holds r.mu.
func (r *Repo) connected(id gitobj.ID, quarantine map[gitobj.ID]gitobj.Object) bool {
	seen := map[gitobj.ID]bool{}
	stack := []gitobj.ID{id}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		o, ok := quarantine[id]
		if !ok {
			if _, stored := r.objects[id]; stored {
				continue
			}
			return false
		}
		switch o.Type {
		case gitobj.TypeCommit:
			c, _ := gitobj.DecodeCommit(o.Data)
			stack = append(append(stack, c.Tree), c.Parents...)
		case gitobj.TypeTree:
			entries, _ := gitobj.DecodeTree(o.Data)
			for _, e := range entries {
				if e.Mode != gitobj.ModeSubmodule {
					stack = append(stack, e.ID)
				}
			}
		case gitobj.TypeTag:
			t, _ := gitobj.DecodeTag(o.Data)
			stack = append(stack, t.Object)
		}
	}
	return true
}

// upload serves a v2 fetch of single commits: "want" lines with "deepen
// 1" and "filter tree:0".
func (g *gitFront) upload(w http.ResponseWriter, r *http.Request, repo *Repo) {
	req, err := gitwire.ParseV2Request(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		WriteMessage(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	if req.Command != "fetch" {
		_ = gitwire.WriteError(w, "fakeforge: only fetch is supported")
		return
	}
	var wants []gitobj.ID
	deepen, filter := false, false
	for _, a := range req.Args {
		switch {
		case strings.HasPrefix(a, "want "):
			id, err := gitobj.ParseID(strings.TrimPrefix(a, "want "))
			if err != nil {
				_ = gitwire.WriteError(w, "fakeforge: bad want")
				return
			}
			wants = append(wants, id)
		case a == "deepen 1":
			deepen = true
		case a == "filter tree:0":
			filter = !g.opts.NoFilter
		}
	}
	if len(wants) == 0 || !deepen || !filter {
		_ = gitwire.WriteError(w, "fakeforge: only single-commit fetches with deepen 1 and filter tree:0")
		return
	}
	var objs []gitobj.Object
	var shallow []gitobj.ID
	for _, id := range wants {
		c, err := repo.ReadCommit(id)
		if err != nil || !repo.reachable(id) {
			_ = gitwire.WriteError(w, "upload-pack: not our ref "+id.String())
			return
		}
		o, _ := repo.Object(id)
		objs = append(objs, o)
		if len(c.Parents) > 0 {
			shallow = append(shallow, id)
		}
	}
	_ = gitwire.WriteFetchResponse(w, shallow, objs)
}

// reachable reports whether commit id is reachable from a ref.
func (r *Repo) reachable(id gitobj.ID) bool {
	for _, ref := range r.Refs("refs/") {
		if ok, err := r.IsAncestor(id, ref.ID); err == nil && ok {
			return true
		}
	}
	return false
}
