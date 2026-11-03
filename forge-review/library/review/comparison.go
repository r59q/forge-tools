package review

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
	"github.com/r59q/forge-tools/forge-core/forgeapi/linediff"
)

func captureComparison(ctx context.Context, session *forgeauth.Session, repo string, in Input, out *Subject) error {
	if in.Ref.Number != 0 {
		return ErrInvalid
	}
	base, err := resolve(ctx, session, repo, in.From)
	if err != nil {
		return err
	}
	head, err := resolve(ctx, session, repo, in.To)
	if err != nil {
		return err
	}
	out.Revision = Revision{Base: base, Target: base, Head: head}

	switch session.Kind {
	case forgeauth.GitHub:
		return githubComparison(ctx, session, repo, out)
	case forgeauth.GitLab:
		return gitlabComparison(ctx, session, repo, out)
	case forgeauth.Forgejo:
		return forgejoComparison(ctx, session, repo, out)
	default:
		return nil
	}
}

func githubComparison(ctx context.Context, session *forgeauth.Session, repo string, out *Subject) error {
	endpoint := repo + "/compare/" + out.Revision.Base + "..." + out.Revision.Head
	var info struct {
		MergeBase struct{ SHA string } `json:"merge_base_commit"`
	}
	if err := forgerest.Send(ctx, session, "GET", endpoint+"?per_page=1", nil, &info); err != nil {
		return err
	}
	if !commitID(info.MergeBase.SHA) {
		return ErrInvalid
	}
	out.Revision.Base = info.MergeBase.SHA
	patch, err := raw(ctx, session, endpoint, "application/vnd.github.diff")
	out.Patch = patch
	return err
}

type gitlabComparisonFile struct {
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
	Diff      string
	NewFile   bool `json:"new_file"`
	Deleted   bool `json:"deleted_file"`
	TooLarge  bool `json:"too_large"`
	Collapsed bool
}

func gitlabComparison(ctx context.Context, session *forgeauth.Session, repo string, out *Subject) error {
	var response struct {
		Diffs   []gitlabComparisonFile
		Timeout bool `json:"compare_timeout"`
	}
	query := url.Values{"from": {out.Revision.Base}, "to": {out.Revision.Head}, "straight": {"true"}}
	if err := forgerest.Send(ctx, session, "GET", repo+"/repository/compare?"+query.Encode(), nil, &response); err != nil {
		return err
	}
	if response.Timeout {
		return ErrTooLarge
	}

	var patch strings.Builder
	for _, file := range response.Diffs {
		if err := file.writePatch(&patch); err != nil {
			return err
		}
		if patch.Len() > maxSnapshot {
			return ErrTooLarge
		}
	}
	out.Patch = patch.String()
	return nil
}

func (file gitlabComparisonFile) writePatch(patch *strings.Builder) error {
	if file.TooLarge || file.Collapsed {
		return ErrTooLarge
	}
	if !filePath(file.OldPath) || !filePath(file.NewPath) {
		return ErrInvalid
	}
	oldPath, newPath := "a/"+file.OldPath, "b/"+file.NewPath
	if file.NewFile {
		oldPath = devNull
	}
	if file.Deleted {
		newPath = devNull
	}
	fmt.Fprintf(patch, "diff --git %s %s\n--- %s\n+++ %s\n",
		strconv.Quote("a/"+file.OldPath), strconv.Quote("b/"+file.NewPath),
		strconv.Quote(oldPath), strconv.Quote(newPath))
	if file.Diff == "" {
		patch.WriteString("Binary files " + strconv.Quote(oldPath) + " and " + strconv.Quote(newPath) + " differ\n")
	} else {
		patch.WriteString(file.Diff)
		if !strings.HasSuffix(file.Diff, "\n") {
			patch.WriteByte('\n')
		}
	}
	return nil
}

func forgejoComparison(ctx context.Context, s *forgeauth.Session, repo string, out *Subject) error {
	var v struct{ Files []struct{ Filename string } }
	if err := forgerest.Send(ctx, s, "GET", repo+"/compare/"+out.Revision.Base+".."+out.Revision.Head, nil, &v); err != nil {
		return err
	}
	if v.Files == nil {
		return ErrUnsupported
	}

	var b strings.Builder
	size := 0

	// The comparison API repeats paths affected by multiple commits.
	seen := make(map[string]bool, len(v.Files))
	for _, f := range v.Files {
		if seen[f.Filename] {
			continue
		}

		seen[f.Filename] = true

		file, err := fetchForgejoFile(ctx, s, repo, out.Revision, f.Filename, &size)
		if err != nil {
			return err
		}
		if err = file.writePatch(&b, out); err != nil {
			return err
		}

		if b.Len()+size > maxSnapshot {
			return ErrTooLarge
		}
	}

	out.Patch = b.String()

	return nil
}

// forgejoFile holds both sides of one compared file. Absent sides have the
// path devNull and empty contents.
type forgejoFile struct {
	name     string
	paths    [2]string
	contents [2]string
}

// fetchForgejoFile downloads a file at the base and head revisions, adding
// the downloaded bytes to size.
func fetchForgejoFile(
	ctx context.Context,
	s *forgeauth.Session,
	repo string,
	rev Revision,
	name string,
	size *int,
) (forgejoFile, error) {
	if !filePath(name) {
		return forgejoFile{}, ErrInvalid
	}

	f := forgejoFile{name: name, paths: [2]string{"a/" + name, "b/" + name}}
	for i, ref := range []string{rev.Base, rev.Head} {
		text, err := raw(ctx, s, repo+"/raw/"+escapePath(name)+"?ref="+ref, "")
		if errors.Is(err, forgehttp.ErrNotFound) {
			f.paths[i] = devNull
			continue
		}

		if err != nil {
			return f, err
		}

		f.contents[i] = text
		*size += len(text)
		if *size > maxSnapshot {
			return f, ErrTooLarge
		}
	}

	if f.paths[0] == devNull && f.paths[1] == devNull {
		return f, ErrInvalid
	}

	return f, nil
}

func (f forgejoFile) binary() bool {
	for _, text := range f.contents {
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return true
		}
	}

	return false
}

// writePatch appends the file's diff to b and its text sides to out.Context.
func (f forgejoFile) writePatch(b *strings.Builder, out *Subject) error {
	a, z := strconv.Quote("a/"+f.name), strconv.Quote("b/"+f.name)
	fmt.Fprintf(b, "diff --git %s %s\n--- %s\n+++ %s\n", a, z, strconv.Quote(f.paths[0]), strconv.Quote(f.paths[1]))

	if f.binary() {
		fmt.Fprintf(b, "Binary files %s and %s differ\n", a, z)
		return nil
	}

	for i, text := range f.contents {
		if len(textLines(text)) > maxLines {
			return ErrTooLarge
		}
		if f.paths[i] != devNull {
			out.Context = append(out.Context, FileContext{Path: f.name, Side: []string{"old", "new"}[i], Content: text})
		}
	}

	patch, _, _ := linediff.Hunks([]byte(f.contents[0]), []byte(f.contents[1]), 3)
	b.WriteString(patch)

	return nil
}
