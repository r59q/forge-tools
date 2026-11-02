package code

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Source is one forge account, usually a forge-connector connection.
type Source struct {
	// ID names the source, such as a connection ID. It must be unique
	// within a Client.
	ID string
	// Session is an open forgeauth session for the forge's REST API. Its
	// Kind selects the forge. Its BaseURL is a forge API URL or a proxy
	// connection URL such as http://127.0.0.1:8081/c/{connectionID}.
	Session *forgeauth.Session
	// Git reaches the forge's Git HTTP endpoints for pushes. If nil,
	// writes return ErrUnsupported.
	Git *forgeauth.GitRemote
}

// RepositoryRef names a repository on one source.
type RepositoryRef struct {
	// Source is a Source.ID. It is required.
	Source string
	// Repository is the repository's full path, such as "owner/name" or
	// "group/sub/project". Do not URL-encode it.
	Repository string
}

// OID is a Git object ID: 40 (SHA-1) or 64 (SHA-256) lowercase hex digits.
type OID string

// ZeroOID is the all-zero object ID. In writes it means a ref that does
// not exist.
const ZeroOID OID = "0000000000000000000000000000000000000000"

// Valid reports whether o is a full, lowercase SHA-1 or SHA-256 object ID.
func (o OID) Valid() bool {
	if len(o) != 40 && len(o) != 64 {
		return false
	}
	for i := 0; i < len(o); i++ {
		c := o[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// zero reports whether o is an all-zero object ID of either length.
func (o OID) zero() bool {
	if !o.Valid() {
		return false
	}
	for i := 0; i < len(o); i++ {
		if o[i] != '0' {
			return false
		}
	}
	return true
}

// FileMode is a Git tree entry mode.
type FileMode uint32

// Git tree entry modes.
const (
	// ModeTree is a directory.
	ModeTree FileMode = 0o040000
	// ModeFile is a regular file.
	ModeFile FileMode = 0o100644
	// ModeExec is an executable file.
	ModeExec FileMode = 0o100755
	// ModeSymlink is a symbolic link; its blob holds the target.
	ModeSymlink FileMode = 0o120000
	// ModeSubmodule is a submodule; its ID is a commit in another
	// repository.
	ModeSubmodule FileMode = 0o160000
)

// String returns the mode as six octal digits, such as "100644".
func (m FileMode) String() string { return fmt.Sprintf("%06o", uint32(m)) }

// valid reports whether m is one of the Git modes above.
func (m FileMode) valid() bool {
	switch m {
	case ModeTree, ModeFile, ModeExec, ModeSymlink, ModeSubmodule:
		return true
	}
	return false
}

// ParseFileMode parses an octal mode such as "100644" or "40000". It
// accepts only the Git modes listed above.
func ParseFileMode(s string) (FileMode, error) {
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || len(s) > 6 || !FileMode(n).valid() {
		return 0, fmt.Errorf("%w: file mode %q", ErrInvalid, s)
	}
	return FileMode(n), nil
}

// ObjectType is a Git object type.
type ObjectType string

// Git object types.
const (
	// ObjectBlob is file content.
	ObjectBlob ObjectType = "blob"
	// ObjectTree is a directory listing.
	ObjectTree ObjectType = "tree"
	// ObjectCommit is a commit. Tree entries of this type are submodules.
	ObjectCommit ObjectType = "commit"
	// ObjectTag is an annotated tag.
	ObjectTag ObjectType = "tag"
)

// typeOf returns the object type of a tree entry with mode m.
func typeOf(m FileMode) ObjectType {
	switch m {
	case ModeTree:
		return ObjectTree
	case ModeSubmodule:
		return ObjectCommit
	}
	return ObjectBlob
}

// Signature is a commit author or committer.
type Signature struct {
	// Name is the person's name.
	Name string
	// Email is the person's email address.
	Email string
	// When is the time of the signature.
	When time.Time
}

// Commit is a Git commit.
type Commit struct {
	// ID is the commit's object ID.
	ID OID
	// Tree is the commit's root tree. It is "" on GitLab, which does not
	// report it.
	Tree OID
	// Parents are the parent commits, first parent first.
	Parents []OID
	// Author wrote the change.
	Author Signature
	// Committer made the commit.
	Committer Signature
	// Message is the full commit message without its trailing newline,
	// which GitHub strips, so it is the same on every forge. GitLab
	// replaces invalid UTF-8.
	Message string
}

// Resolved is a revision resolved to a commit.
type Resolved struct {
	// Commit is the commit the revision names.
	Commit OID
	// Ref is the full ref name that matched, such as "refs/heads/main",
	// or "" if the revision started with an object ID.
	Ref string
}

// Ref is a branch or tag.
type Ref struct {
	// Name is the full ref name, such as "refs/heads/main".
	Name string
	// Target is the object the ref points to: the tag object for an
	// annotated tag, otherwise the commit.
	Target OID
	// Commit is the commit the ref points to after peeling tags. It is ""
	// if a tag points to something other than a commit.
	Commit OID
}

// RefList is the result of Client.ListRefs.
type RefList struct {
	// Default is the default branch's full ref name, or "" if the
	// repository has none.
	Default string
	// Refs are the branches and tags, sorted by name.
	Refs []Ref
	// Truncated reports that more refs exist than were returned.
	Truncated bool
}

// Entry is one tree entry.
type Entry struct {
	// Name is the entry's base name.
	Name string
	// Path is the entry's path from the repository root.
	Path string
	// Mode is the entry's Git mode.
	Mode FileMode
	// Type is the entry's object type.
	Type ObjectType
	// ID is the entry's object ID.
	ID OID
	// Size is a blob's size in bytes, or -1 if unknown or not a blob.
	Size int64
}

// Tree is a directory listing at one commit.
type Tree struct {
	// Commit is the commit the tree was read from.
	Commit OID
	// ID is the directory's tree ID. It is "" on GitLab.
	ID OID
	// Path is the listed directory, "" for the root.
	Path string
	// Entries are the directory's entries, or every entry below it for a
	// recursive listing, sorted by Path.
	Entries []Entry
	// Truncated reports that the listing hit MaxEntries or the call limit.
	Truncated bool
}

// PathInfo describes one path at one commit.
type PathInfo struct {
	// Commit is the commit the path was read from.
	Commit OID
	Entry
}

// Blob describes file content.
type Blob struct {
	// ID is the blob's object ID.
	ID OID
	// Commit is the commit the blob was read from, or "" for ReadBlob.
	Commit OID
	// Path is the file's path, or "" for ReadBlob.
	Path string
	// Size is the content size in bytes, or -1 if unknown.
	Size int64
	// LFSPointer reports that the content is a Git LFS pointer. LFS
	// content itself is never fetched.
	LFSPointer bool
}

// Reader streams a blob's content. The caller must close it. Reading past
// the client's MaxBlobBytes fails with an error wrapping ErrTooLarge.
type Reader struct {
	Blob
	io.ReadCloser
}

// ChangeStatus is how a path changed between two trees.
type ChangeStatus string

// Change statuses.
const (
	// Added means the path is new.
	Added ChangeStatus = "added"
	// Deleted means the path was removed.
	Deleted ChangeStatus = "deleted"
	// Modified means the content or mode changed.
	Modified ChangeStatus = "modified"
	// Renamed means identical content moved from OldPath to Path.
	Renamed ChangeStatus = "renamed"
	// TypeChanged means the entry changed type, such as file to symlink.
	TypeChanged ChangeStatus = "type_changed"
)

// Change is one changed path in a Diff.
type Change struct {
	// Status says how the path changed.
	Status ChangeStatus
	// OldPath is the path in the base, "" if added.
	OldPath string
	// Path is the path in the head, "" if deleted.
	Path string
	// OldMode is the mode in the base, 0 if added.
	OldMode FileMode
	// Mode is the mode in the head, 0 if deleted.
	Mode FileMode
	// OldID is the object ID in the base, "" if added.
	OldID OID
	// ID is the object ID in the head, "" if deleted.
	ID OID
	// Binary reports binary content, so no patch is made.
	Binary bool
	// Additions is the number of added lines, if the lines were diffed.
	Additions int
	// Deletions is the number of deleted lines, if the lines were diffed.
	Deletions int
	// Patch is the unified diff hunks, without file headers, if patches
	// were asked for.
	Patch string
	// PatchOmitted reports that a patch was asked for but left out, as it
	// hit MaxPatchFiles, MaxPatchBytes, the patch read budget or the call
	// limit.
	PatchOmitted bool
}

// Diff is the result of Client.Compare.
type Diff struct {
	// Base is the resolved base commit.
	Base OID
	// Head is the resolved head commit.
	Head OID
	// MergeBase is the commit the head was compared with in MergeBase
	// mode, or "" in Direct mode.
	MergeBase OID
	// Changes are the changed paths, sorted by Path (OldPath if deleted).
	Changes []Change
	// Truncated reports that the diff hit MaxChanges or the call limit.
	Truncated bool
}

// Capabilities says what a source supports.
type Capabilities struct {
	// TreeIDs reports that trees carry their IDs. It is false on GitLab.
	TreeIDs bool
	// MergeBase reports that MergeBase and Compare in MergeBase mode work.
	// On Forgejo, criss-cross histories return ErrUnsupported.
	MergeBase bool
	// Writes reports that the source has a Git remote. Whether a
	// repository allows atomic or deleting pushes is checked on write.
	Writes bool
}

// RefsOptions configure Client.ListRefs.
type RefsOptions struct {
	// Prefix is "refs/heads/", "refs/tags/", or "" for both.
	Prefix string
	// Limit caps the number of refs. The default is 10000.
	Limit int
}

// HistoryOptions configure Client.History.
type HistoryOptions struct {
	// Revision is where the history starts. The default is HEAD. It is
	// not resolved again when Cursor is set.
	Revision string
	// Path keeps only commits that change this file or directory. With
	// Cursor set it must be empty or the cursor's path.
	Path string
	// PageSize is the number of commits per page: default 50, at most
	// 100. With Cursor set it must be zero or the cursor's page size.
	PageSize int
	// Cursor continues from a previous HistoryPage.Next.
	Cursor string
}

// HistoryPage is one page of Client.History.
type HistoryPage struct {
	// Tip is the commit the history starts from. All pages of one
	// listing share it.
	Tip OID
	// Commits are in the forge's log order, newest first.
	Commits []Commit
	// Next is the cursor for the next page, or "" after the last page.
	Next string
}

// TreeOptions configure Client.Tree.
type TreeOptions struct {
	// Path is the directory to list, "" for the root.
	Path string
	// Recursive lists every entry below Path, not only its children.
	Recursive bool
	// MaxEntries caps the number of entries. The default is 100000.
	MaxEntries int
}

// CompareMode selects what Client.Compare compares the head with.
type CompareMode string

// Compare modes.
const (
	// MergeBase compares the head with the merge base of base and head,
	// like "git diff base...head". It is the default.
	MergeBase CompareMode = "merge_base"
	// Direct compares the head with the base, like "git diff base..head".
	Direct CompareMode = "direct"
)

// CompareOptions configure Client.Compare.
type CompareOptions struct {
	// Mode is MergeBase (default) or Direct.
	Mode CompareMode
	// Paths keeps only changes at or below these paths.
	Paths []string
	// Patches adds unified diffs to text changes.
	Patches bool
	// Context is the number of context lines in patches. Zero means the
	// default of 3; a negative value means none.
	Context int
	// MaxChanges caps the number of changes. The default is 3000.
	MaxChanges int
	// MaxPatchFiles caps how many changes get a patch. The default is
	// 100; larger values than 1000 are lowered to 1000.
	MaxPatchFiles int
	// MaxPatchBytes caps one patch's size. The default is 256 KiB; larger
	// values than 1 MiB are lowered to 1 MiB. Blobs over 16 times this
	// get no patch, and one Compare reads at most 64 MiB of blobs for
	// patches.
	MaxPatchBytes int
}

// Limits cap the work of one Client call. Zero fields take the defaults.
type Limits struct {
	// MaxBlobBytes caps the content one Reader returns. The default is
	// 100 MiB.
	MaxBlobBytes int64
	// MaxCalls caps the forge requests one call makes. The default is
	// 1000. Listings stop early and report Truncated; other calls fail
	// with ErrTooLarge.
	MaxCalls int
	// MaxWriteBytes caps the file content of one commit. The default is
	// 64 MiB.
	MaxWriteBytes int64
}

// Default limits.
const (
	defaultMaxBlobBytes  = 100 << 20
	defaultMaxCalls      = 1000
	defaultMaxWriteBytes = 64 << 20
)

// withDefaults fills zero limits and rejects negative ones.
func (l Limits) withDefaults() (Limits, error) {
	if l.MaxBlobBytes < 0 || l.MaxCalls < 0 || l.MaxWriteBytes < 0 {
		return Limits{}, fmt.Errorf("%w: negative limit", ErrInvalid)
	}
	if l.MaxBlobBytes == 0 {
		l.MaxBlobBytes = defaultMaxBlobBytes
	}
	if l.MaxCalls == 0 {
		l.MaxCalls = defaultMaxCalls
	}
	if l.MaxWriteBytes == 0 {
		l.MaxWriteBytes = defaultMaxWriteBytes
	}
	return l, nil
}

// FileChange is one file to add, change or delete in a NewCommit.
type FileChange struct {
	// Path is the file's path from the repository root.
	Path string
	// Delete removes the file. Mode, Content and Submodule must be empty.
	Delete bool
	// Mode is the new mode. Zero keeps the existing mode, or uses
	// ModeFile for a new file. For ModeSymlink, Content is the target.
	Mode FileMode
	// Content is the new file content.
	Content []byte
	// Submodule is the commit of a ModeSubmodule entry.
	Submodule OID
}

// NewCommit is a commit to create and push.
type NewCommit struct {
	// Parents are the parent commits. None makes a root commit and two or
	// more a merge. Changes apply to the tree of Parents[0].
	Parents []OID
	// Changes are the file changes, at most one per path.
	Changes []FileChange
	// Author wrote the change. A zero When means now, in UTC.
	Author Signature
	// Committer made the commit. If zero, it is Author.
	Committer Signature
	// Message is the commit message. It is required.
	Message string
	// Ref is the branch or tag to move to the new commit, such as
	// "refs/heads/main". It is required.
	Ref string
	// Old is Ref's expected current value; ZeroOID means Ref must not
	// exist. The push fails with ErrStale otherwise.
	Old OID
}

// RefUpdate moves, creates or deletes one ref.
type RefUpdate struct {
	// Name is the full ref name, such as "refs/heads/main".
	Name string
	// Old is the expected current value; ZeroOID means it must not exist.
	Old OID
	// New is the new value; ZeroOID deletes the ref.
	New OID
}
