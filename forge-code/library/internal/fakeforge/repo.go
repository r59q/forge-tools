package fakeforge

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// Repo is an in-memory Git repository: objects keyed by their real SHA-1
// IDs, refs, a default branch and a private flag. It is safe for
// concurrent use.
type Repo struct {
	mu      sync.RWMutex
	objects map[gitobj.ID]gitobj.Object
	refs    map[string]gitobj.ID
	head    string
	private bool
	id      int
}

// NewRepo returns an empty public repository whose default branch is
// "main".
func NewRepo() *Repo {
	return &Repo{
		objects: map[gitobj.ID]gitobj.Object{},
		refs:    map[string]gitobj.ID{},
		head:    "main",
	}
}

// ID returns the numeric ID the forge gave the repository, or 0 if it was
// never added to a forge.
func (r *Repo) ID() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.id
}

// DefaultBranch returns the default branch's short name, such as "main".
func (r *Repo) DefaultBranch() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.head
}

// SetDefaultBranch sets the default branch's short name.
func (r *Repo) SetDefaultBranch(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.head = name
}

// Head returns the commit of the default branch.
func (r *Repo) Head() (gitobj.ID, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.refs["refs/heads/"+r.head]
	return id, ok
}

// Private reports whether the repository is private.
func (r *Repo) Private() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.private
}

// SetPrivate marks the repository private or public.
func (r *Repo) SetPrivate(private bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.private = private
}

// Put stores an object after checking that its content parses as its
// type. Objects it refers to need not exist. Storing an object twice is a
// no-op.
func (r *Repo) Put(o gitobj.Object) (gitobj.ID, error) {
	if err := checkObject(o); err != nil {
		return gitobj.ZeroID, err
	}
	id := o.ID()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.objects[id]; !ok {
		r.objects[id] = gitobj.Object{Type: o.Type, Data: slices.Clone(o.Data)}
	}
	return id, nil
}

func checkObject(o gitobj.Object) error {
	var err error
	switch o.Type {
	case gitobj.TypeBlob:
	case gitobj.TypeTree:
		_, err = gitobj.DecodeTree(o.Data)
	case gitobj.TypeCommit:
		_, err = gitobj.DecodeCommit(o.Data)
	case gitobj.TypeTag:
		_, err = gitobj.DecodeTag(o.Data)
	default:
		err = fmt.Errorf("%w: object type %q", gitobj.ErrInvalid, o.Type)
	}
	return err
}

// Object returns a copy of the object with the given ID.
func (r *Repo) Object(id gitobj.ID) (gitobj.Object, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, ok := r.objects[id]
	if !ok {
		return gitobj.Object{}, false
	}
	return gitobj.Object{Type: o.Type, Data: slices.Clone(o.Data)}, true
}

// IDs returns the IDs of all stored objects, sorted.
func (r *Repo) IDs() []gitobj.ID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.SortedFunc(maps.Keys(r.objects), func(a, b gitobj.ID) int {
		return bytes.Compare(a[:], b[:])
	})
}

// ReadBlob returns a copy of a blob's content.
func (r *Repo) ReadBlob(id gitobj.ID) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, err := r.get(id, gitobj.TypeBlob)
	if err != nil {
		return nil, err
	}
	return slices.Clone(o.Data), nil
}

// ReadTree returns a tree's entries in stored order.
func (r *Repo) ReadTree(id gitobj.ID) ([]gitobj.TreeEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tree(id)
}

// ReadCommit returns a decoded commit.
func (r *Repo) ReadCommit(id gitobj.ID) (gitobj.Commit, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.commit(id)
}

// ReadTag returns a decoded annotated tag.
func (r *Repo) ReadTag(id gitobj.ID) (gitobj.Tag, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, err := r.get(id, gitobj.TypeTag)
	if err != nil {
		return gitobj.Tag{}, err
	}
	return gitobj.DecodeTag(o.Data)
}

// Peel follows annotated tags from id to the first object that is not a
// tag, and returns it with its type.
func (r *Repo) Peel(id gitobj.ID) (gitobj.ID, gitobj.Type, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.peel(id)
}

// PeelCommit is Peel, but the result must be a commit.
func (r *Repo) PeelCommit(id gitobj.ID) (gitobj.ID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.peelCommit(id)
}

// get returns the object id, which must have type t. The caller holds mu.
func (r *Repo) get(id gitobj.ID, t gitobj.Type) (gitobj.Object, error) {
	o, ok := r.objects[id]
	if !ok {
		return o, fmt.Errorf("%w: object %s", ErrNotFound, id)
	}
	if o.Type != t {
		return o, fmt.Errorf("%w: %s is a %s, not a %s", ErrWrongType, id, o.Type, t)
	}
	return o, nil
}

func (r *Repo) tree(id gitobj.ID) ([]gitobj.TreeEntry, error) {
	o, err := r.get(id, gitobj.TypeTree)
	if err != nil {
		return nil, err
	}
	return gitobj.DecodeTree(o.Data)
}

func (r *Repo) commit(id gitobj.ID) (gitobj.Commit, error) {
	o, err := r.get(id, gitobj.TypeCommit)
	if err != nil {
		return gitobj.Commit{}, err
	}
	return gitobj.DecodeCommit(o.Data)
}

// maxPeel bounds tag chains.
const maxPeel = 100

func (r *Repo) peel(id gitobj.ID) (gitobj.ID, gitobj.Type, error) {
	for range maxPeel {
		o, ok := r.objects[id]
		if !ok {
			return gitobj.ZeroID, "", fmt.Errorf("%w: object %s", ErrNotFound, id)
		}
		if o.Type != gitobj.TypeTag {
			return id, o.Type, nil
		}
		t, err := gitobj.DecodeTag(o.Data)
		if err != nil {
			return gitobj.ZeroID, "", err
		}
		id = t.Object
	}
	return gitobj.ZeroID, "", fmt.Errorf("%w: tag chain longer than %d", ErrWrongType, maxPeel)
}

func (r *Repo) peelCommit(id gitobj.ID) (gitobj.ID, error) {
	p, t, err := r.peel(id)
	if err != nil {
		return gitobj.ZeroID, err
	}
	if t != gitobj.TypeCommit {
		return gitobj.ZeroID, fmt.Errorf("%w: %s peels to a %s", ErrWrongType, id, t)
	}
	return p, nil
}

// must panics on builder errors, which are bugs in the test.
func must(id gitobj.ID, err error) gitobj.ID {
	if err != nil {
		panic("fakeforge: " + err.Error())
	}
	return id
}

// WriteBlob stores a blob and returns its ID.
func (r *Repo) WriteBlob(data []byte) gitobj.ID {
	return must(r.Put(gitobj.Object{Type: gitobj.TypeBlob, Data: data}))
}

// WriteTree stores a tree with entries in canonical order and returns its
// ID. Every entry other than a submodule must name a stored object of the
// matching type. It panics on invalid input.
func (r *Repo) WriteTree(entries ...gitobj.TreeEntry) gitobj.ID {
	for _, e := range entries {
		if e.Mode == gitobj.ModeSubmodule {
			continue
		}
		r.mu.RLock()
		_, err := r.get(e.ID, e.Mode.Type())
		r.mu.RUnlock()
		if err != nil {
			panic(fmt.Sprintf("fakeforge: tree entry %q: %v", e.Name, err))
		}
	}
	data, err := gitobj.EncodeTree(entries)
	if err != nil {
		panic("fakeforge: " + err.Error())
	}
	return must(r.Put(gitobj.Object{Type: gitobj.TypeTree, Data: data}))
}

// WriteCommit stores a commit and returns its ID. Its tree and parents
// must be stored. It panics on invalid input.
func (r *Repo) WriteCommit(c gitobj.Commit) gitobj.ID {
	r.mu.RLock()
	_, err := r.get(c.Tree, gitobj.TypeTree)
	for _, p := range c.Parents {
		if err == nil {
			_, err = r.get(p, gitobj.TypeCommit)
		}
	}
	r.mu.RUnlock()
	if err != nil {
		panic("fakeforge: commit: " + err.Error())
	}
	data, err := gitobj.EncodeCommit(c)
	if err != nil {
		panic("fakeforge: " + err.Error())
	}
	return must(r.Put(gitobj.Object{Type: gitobj.TypeCommit, Data: data}))
}

// WriteTag stores an annotated tag and returns its ID. The target must be
// stored; an empty t.Type is filled in from it. It panics on invalid
// input.
func (r *Repo) WriteTag(t gitobj.Tag) gitobj.ID {
	o, ok := r.Object(t.Object)
	if !ok {
		panic(fmt.Sprintf("fakeforge: tag %q: target %s not found", t.Name, t.Object))
	}
	if t.Type == "" {
		t.Type = o.Type
	}
	if t.Type != o.Type {
		panic(fmt.Sprintf("fakeforge: tag %q: target is a %s, not a %s", t.Name, o.Type, t.Type))
	}
	data, err := gitobj.EncodeTag(t)
	if err != nil {
		panic("fakeforge: " + err.Error())
	}
	return must(r.Put(gitobj.Object{Type: gitobj.TypeTag, Data: data}))
}

// File is one file for BuildTree.
type File struct {
	// Mode is the entry mode. Zero means gitobj.ModeFile.
	Mode gitobj.Mode
	// Data is the content, or the target of a symlink.
	Data []byte
	// Submodule is the commit of a gitobj.ModeSubmodule entry.
	Submodule gitobj.ID
}

// BuildTree stores the blobs and nested trees for files, keyed by
// slash-separated path, and returns the root tree's ID. It panics on
// invalid paths or a path that is both a file and a directory.
func (r *Repo) BuildTree(files map[string]File) gitobj.ID {
	var entries []gitobj.TreeEntry
	dirs := map[string]map[string]File{}
	for _, p := range slices.Sorted(maps.Keys(files)) {
		f := files[p]
		name, rest, nested := strings.Cut(p, "/")
		if nested {
			if dirs[name] == nil {
				dirs[name] = map[string]File{}
			}
			dirs[name][rest] = f
			continue
		}
		mode := f.Mode
		if mode == 0 {
			mode = gitobj.ModeFile
		}
		var id gitobj.ID
		switch mode {
		case gitobj.ModeSubmodule:
			id = f.Submodule
		case gitobj.ModeTree:
			panic(fmt.Sprintf("fakeforge: BuildTree: %q has mode tree", p))
		default:
			id = r.WriteBlob(f.Data)
		}
		entries = append(entries, gitobj.TreeEntry{Name: name, Mode: mode, ID: id})
	}
	for _, name := range slices.Sorted(maps.Keys(dirs)) {
		if _, clash := files[name]; clash {
			panic(fmt.Sprintf("fakeforge: BuildTree: %q is a file and a directory", name))
		}
		entries = append(entries, gitobj.TreeEntry{Name: name, Mode: gitobj.ModeTree, ID: r.BuildTree(dirs[name])})
	}
	return r.WriteTree(entries...)
}

// Signature returns the test signature used by CommitTree.
func Signature(when time.Time) gitobj.Signature {
	return gitobj.Signature{Name: "Fake Author", Email: "author@example.com", When: when}
}

// CommitTree stores a commit of tree with the given parents, using
// Signature(when) as author and committer, and returns its ID.
func (r *Repo) CommitTree(tree gitobj.ID, when time.Time, message string, parents ...gitobj.ID) gitobj.ID {
	return r.WriteCommit(gitobj.Commit{
		Tree:      tree,
		Parents:   parents,
		Author:    Signature(when),
		Committer: Signature(when),
		Message:   message,
	})
}

// NamedRef is a ref and the object it points to.
type NamedRef struct {
	Name string
	ID   gitobj.ID
}

// Ref returns the object a full ref name, such as "refs/heads/main",
// points to.
func (r *Repo) Ref(name string) (gitobj.ID, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.refs[name]
	return id, ok
}

// Refs returns the refs whose names start with prefix, sorted by name.
func (r *Repo) Refs(prefix string) []NamedRef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []NamedRef
	for name, id := range r.refs {
		if strings.HasPrefix(name, prefix) {
			out = append(out, NamedRef{Name: name, ID: id})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetRef points a ref at id, or deletes it if id is zero. It does not
// check that id exists, so tests can make dangling refs. It panics if
// name does not start with "refs/".
func (r *Repo) SetRef(name string, id gitobj.ID) {
	if !strings.HasPrefix(name, "refs/") {
		panic(fmt.Sprintf("fakeforge: ref %q does not start with refs/", name))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if id.IsZero() {
		delete(r.refs, name)
		return
	}
	r.refs[name] = id
}

// RefUpdate changes a ref from Old to New. A zero Old means the ref must
// not exist; a zero New deletes it.
type RefUpdate struct {
	Name     string
	Old, New gitobj.ID
}

// UpdateRefs applies all updates or none, under one lock. It fails with
// ErrStale if a ref is not at its Old value, and with ErrNotFound if a New
// object is not stored.
func (r *Repo) UpdateRefs(updates ...RefUpdate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, u := range updates {
		if !strings.HasPrefix(u.Name, "refs/") || seen[u.Name] {
			return fmt.Errorf("%w: bad or repeated ref %q", gitobj.ErrInvalid, u.Name)
		}
		seen[u.Name] = true
		if cur := r.refs[u.Name]; cur != u.Old {
			return fmt.Errorf("%w: %s is at %s, not %s", ErrStale, u.Name, cur, u.Old)
		}
		if _, ok := r.objects[u.New]; !u.New.IsZero() && !ok {
			return fmt.Errorf("%w: object %s for %s", ErrNotFound, u.New, u.Name)
		}
	}
	for _, u := range updates {
		if u.New.IsZero() {
			delete(r.refs, u.Name)
		} else {
			r.refs[u.Name] = u.New
		}
	}
	return nil
}
