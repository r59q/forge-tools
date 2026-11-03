package api

import (
	"fmt"
	"math"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/code"
	pb "github.com/r59q/forge-tools/forge-code/service/codepb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// repo returns the repository a request names.
func repo(r interface {
	GetSource() string
	GetRepository() string
}) code.RepositoryRef {
	return code.RepositoryRef{Source: r.GetSource(), Repository: r.GetRepository()}
}

func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

func signature(s code.Signature) *pb.Signature {
	return &pb.Signature{Name: s.Name, Email: s.Email, When: timestamp(s.When)}
}

func oids(ids []code.OID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}

	return out
}

func commit(c code.Commit) *pb.Commit {
	return &pb.Commit{
		Id:        string(c.ID),
		Tree:      string(c.Tree),
		Parents:   oids(c.Parents),
		Author:    signature(c.Author),
		Committer: signature(c.Committer),
		Message:   c.Message,
	}
}

func entry(e code.Entry) *pb.Entry {
	return &pb.Entry{
		Name: e.Name,
		Path: e.Path,
		Mode: e.Mode.String(),
		Type: string(e.Type),
		Id:   string(e.ID),
		Size: e.Size,
	}
}

func blob(b code.Blob) *pb.Blob {
	return &pb.Blob{
		Id:         string(b.ID),
		Commit:     string(b.Commit),
		Path:       b.Path,
		Size:       b.Size,
		LfsPointer: b.LFSPointer,
	}
}

// mode formats a change mode, "" for none.
func mode(m code.FileMode) string {
	if m == 0 {
		return ""
	}

	return m.String()
}

func change(c code.Change) *pb.Change {
	return &pb.Change{
		Status:       string(c.Status),
		OldPath:      c.OldPath,
		Path:         c.Path,
		OldMode:      mode(c.OldMode),
		Mode:         mode(c.Mode),
		OldId:        string(c.OldID),
		Id:           string(c.ID),
		Binary:       c.Binary,
		Additions:    count32(c.Additions),
		Deletions:    count32(c.Deletions),
		Patch:        c.Patch,
		PatchOmitted: c.PatchOmitted,
	}
}

// count32 converts a line count, clamped to [0, math.MaxInt32].
func count32(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	}

	return int32(n)
}

// fromSignature converts a request signature. Nil is the zero Signature.
func fromSignature(s *pb.Signature) (code.Signature, error) {
	if s == nil {
		return code.Signature{}, nil
	}

	out := code.Signature{Name: s.GetName(), Email: s.GetEmail()}
	if s.When != nil {
		if err := s.When.CheckValid(); err != nil {
			return code.Signature{}, fmt.Errorf("%w: signature time", code.ErrInvalid)
		}

		out.When = s.When.AsTime()
	}

	return out, nil
}

func toOIDs(ids []string) []code.OID {
	out := make([]code.OID, len(ids))
	for i, id := range ids {
		out[i] = code.OID(id)
	}

	return out
}

// newCommit converts a CreateCommitRequest. The library checks the
// values.
func newCommit(r *pb.CreateCommitRequest) (code.NewCommit, error) {
	n := code.NewCommit{
		Parents: toOIDs(r.GetParents()),
		Message: r.GetMessage(),
		Ref:     r.GetRef(),
		Old:     code.OID(r.GetOld()),
	}
	var err error
	if n.Author, err = fromSignature(r.GetAuthor()); err != nil {
		return code.NewCommit{}, err
	}
	if n.Committer, err = fromSignature(r.GetCommitter()); err != nil {
		return code.NewCommit{}, err
	}

	for _, c := range r.GetChanges() {
		fc := code.FileChange{Path: c.GetPath(), Delete: c.GetDelete(), Submodule: code.OID(c.GetSubmodule())}
		if c.GetMode() != "" {
			if fc.Mode, err = code.ParseFileMode(c.GetMode()); err != nil {
				return code.NewCommit{}, err
			}
		}
		// Empty content is no content, so it may pair with a delete or a
		// submodule.
		if len(c.GetContent()) > 0 {
			fc.Content = c.GetContent()
		}

		n.Changes = append(n.Changes, fc)
	}

	return n, nil
}

// refUpdates converts the updates of an UpdateRefsRequest.
func refUpdates(r *pb.UpdateRefsRequest) []code.RefUpdate {
	out := make([]code.RefUpdate, len(r.GetUpdates()))
	for i, u := range r.GetUpdates() {
		out[i] = code.RefUpdate{Name: u.GetName(), Old: code.OID(u.GetOld()), New: code.OID(u.GetNew())}
	}

	return out
}

// compareOptions converts the options of a CompareRequest.
func compareOptions(r *pb.CompareRequest) code.CompareOptions {
	return code.CompareOptions{
		Mode:          code.CompareMode(r.GetMode()),
		Paths:         r.GetPaths(),
		Patches:       r.GetPatches(),
		Context:       int(r.GetContext()),
		MaxChanges:    int(r.GetMaxChanges()),
		MaxPatchFiles: int(r.GetMaxPatchFiles()),
		MaxPatchBytes: int(r.GetMaxPatchBytes()),
	}
}
