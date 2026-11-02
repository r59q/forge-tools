package review

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
	"github.com/r59q/forge-tools/forge-core/forgeapi/linediff"
)

func commitID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}
func (c *Client) locate(r Ref) (*forgeauth.Session, string, string, error) {
	s := c.sources[r.Source]
	if s == nil {
		return nil, "", "", fmt.Errorf("%w: source", ErrInvalid)
	}
	p, ok := forgerest.CleanPath(r.Repository)
	if !ok {
		return nil, "", "", ErrInvalid
	}
	repo := "/projects/" + url.PathEscape(p)
	if s.Kind != forgeauth.GitLab {
		parts := strings.Split(p, "/")
		if len(parts) != 2 {
			return nil, "", "", ErrInvalid
		}
		repo = "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	}
	request := repo + "/pulls/" + strconv.FormatInt(r.Number, 10)
	if s.Kind == forgeauth.GitLab {
		request = repo + "/merge_requests/" + strconv.FormatInt(r.Number, 10)
	}
	return s, repo, request, nil
}
func resolve(ctx context.Context, s *forgeauth.Session, repo, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" || ref == "." || ref == ".." || strings.ContainsAny(ref, "\x00\r\n") {
		return "", ErrInvalid
	}
	route := repo + "/commits/"
	if s.Kind == forgeauth.GitLab {
		route = repo + "/repository/commits/"
	}
	if s.Kind == forgeauth.Forgejo {
		route = repo + "/git/commits/"
	}
	var v struct {
		SHA string
		ID  string
	}
	if err := forgerest.Send(ctx, s, "GET", route+url.PathEscape(ref), nil, &v); err != nil {
		return "", err
	}
	sha := v.SHA
	if s.Kind == forgeauth.GitLab {
		sha = v.ID
	}
	if !commitID(sha) {
		return "", ErrInvalid
	}
	return sha, nil
}
func revision(ctx context.Context, s *forgeauth.Session, request string) (Revision, error) {
	var w struct {
		Head      struct{ SHA string }
		Base      struct{ SHA string }
		MergeBase string `json:"merge_base"`
		DiffRefs  struct {
			Base   string `json:"base_sha"`
			Head   string `json:"head_sha"`
			Target string `json:"start_sha"`
		} `json:"diff_refs"`
	}
	if err := forgerest.Send(ctx, s, "GET", request, nil, &w); err != nil {
		return Revision{}, err
	}
	r := Revision{Head: w.Head.SHA, Target: w.Base.SHA, Base: w.MergeBase}
	if s.Kind == forgeauth.GitLab {
		r = Revision{Base: w.DiffRefs.Base, Head: w.DiffRefs.Head, Target: w.DiffRefs.Target}
	}
	if !commitID(r.Head) || !commitID(r.Target) || (r.Base != "" && !commitID(r.Base)) {
		return Revision{}, ErrInvalid
	}
	return r, nil
}
func raw(ctx context.Context, s *forgeauth.Session, path, accept string) (string, error) {
	h := http.Header{}
	if accept != "" {
		h.Set("Accept", accept)
	}
	resp, err := forgerest.Open(ctx, s, "GET", path, h)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSnapshot+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxSnapshot {
		return "", ErrTooLarge
	}
	return string(b), nil
}
func (c *Client) capture(ctx context.Context, in Input, out *Subject) error {
	s, repo, request, err := c.locate(in.Ref)
	if err != nil {
		return err
	}
	out.Completeness = "unknown"
	if in.Kind == "comparison" {
		if in.Ref.Number != 0 {
			return ErrInvalid
		}
		base, err := resolve(ctx, s, repo, in.From)
		if err != nil {
			return err
		}
		head, err := resolve(ctx, s, repo, in.To)
		if err != nil {
			return err
		}
		out.Revision = Revision{Base: base, Target: base, Head: head}
		switch s.Kind {
		case forgeauth.GitHub:
			endpoint := repo + "/compare/" + base + "..." + head
			var info struct {
				MergeBase struct{ SHA string } `json:"merge_base_commit"`
			}
			if err = forgerest.Send(ctx, s, "GET", endpoint+"?per_page=1", nil, &info); err != nil {
				return err
			}
			if !commitID(info.MergeBase.SHA) {
				return ErrInvalid
			}
			out.Revision.Base = info.MergeBase.SHA
			out.Patch, err = raw(ctx, s, endpoint, "application/vnd.github.diff")
		case forgeauth.GitLab:
			var v struct {
				Diffs []struct {
					OldPath   string `json:"old_path"`
					NewPath   string `json:"new_path"`
					Diff      string
					NewFile   bool `json:"new_file"`
					Deleted   bool `json:"deleted_file"`
					TooLarge  bool `json:"too_large"`
					Collapsed bool
				}
				Timeout bool `json:"compare_timeout"`
			}
			q := url.Values{"from": {base}, "to": {head}, "straight": {"true"}}
			if err = forgerest.Send(ctx, s, "GET", repo+"/repository/compare?"+q.Encode(), nil, &v); err != nil {
				return err
			}
			if v.Timeout {
				return ErrTooLarge
			}
			var b strings.Builder
			for _, f := range v.Diffs {
				if f.TooLarge || f.Collapsed {
					return ErrTooLarge
				}
				if !filePath(f.OldPath) || !filePath(f.NewPath) {
					return ErrInvalid
				}
				a, z := "a/"+f.OldPath, "b/"+f.NewPath
				if f.NewFile {
					a = "/dev/null"
				}
				if f.Deleted {
					z = "/dev/null"
				}
				fmt.Fprintf(&b, "diff --git %s %s\n--- %s\n+++ %s\n", strconv.Quote("a/"+f.OldPath), strconv.Quote("b/"+f.NewPath), strconv.Quote(a), strconv.Quote(z))
				if f.Diff == "" {
					b.WriteString("Binary files " + strconv.Quote(a) + " and " + strconv.Quote(z) + " differ\n")
				} else {
					b.WriteString(f.Diff)
					if !strings.HasSuffix(f.Diff, "\n") {
						b.WriteByte('\n')
					}
				}
				if b.Len() > maxSnapshot {
					return ErrTooLarge
				}
			}
			out.Patch = b.String()
		case forgeauth.Forgejo:
			err = forgejoComparison(ctx, s, repo, out)
		}
		return err
	}
	if in.Ref.Number <= 0 || in.From != "" || in.To != "" {
		return ErrInvalid
	}
	before, err := revision(ctx, s, request)
	if err != nil {
		return err
	}
	endpoint, accept := request, "application/vnd.github.diff"
	switch s.Kind {
	case forgeauth.GitLab:
		endpoint += "/raw_diffs"
		accept = ""
	case forgeauth.Forgejo:
		endpoint += ".diff"
		accept = ""
	}
	out.Patch, err = raw(ctx, s, endpoint, accept)
	if err != nil {
		return err
	}
	after, err := revision(ctx, s, request)
	if err != nil {
		return err
	}
	if before != after {
		return ErrConflict
	}
	out.Revision = before
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
	for _, f := range v.Files {
		if !filePath(f.Filename) {
			return ErrInvalid
		}
		a, z := "a/"+f.Filename, "b/"+f.Filename
		contents := [2]string{}
		paths := [2]string{a, z}
		for i, rev := range []string{out.Revision.Base, out.Revision.Head} {
			segments := strings.Split(f.Filename, "/")
			for j := range segments {
				segments[j] = url.PathEscape(segments[j])
			}
			text, err := raw(ctx, s, repo+"/raw/"+strings.Join(segments, "/")+"?ref="+rev, "")
			if errors.Is(err, forgehttp.ErrNotFound) {
				paths[i] = "/dev/null"
				continue
			}
			if err != nil {
				return err
			}
			contents[i] = text
			size += len(text)
			if size > maxSnapshot {
				return ErrTooLarge
			}
		}
		if paths[0] == "/dev/null" && paths[1] == "/dev/null" {
			return ErrInvalid
		}
		fmt.Fprintf(&b, "diff --git %s %s\n--- %s\n+++ %s\n", strconv.Quote(a), strconv.Quote(z), strconv.Quote(paths[0]), strconv.Quote(paths[1]))
		binary := false
		for _, text := range contents {
			if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
				binary = true
			}
		}
		if binary {
			fmt.Fprintf(&b, "Binary files %s and %s differ\n", strconv.Quote(a), strconv.Quote(z))
		} else {
			for i, text := range contents {
				if len(textLines(text)) > maxLines {
					return ErrTooLarge
				}
				if paths[i] != "/dev/null" {
					out.Context = append(out.Context, FileContext{Path: f.Filename, Side: []string{"old", "new"}[i], Content: text})
				}
			}
			patch, _, _ := linediff.Hunks([]byte(contents[0]), []byte(contents[1]), 3)
			b.WriteString(patch)
		}
		if b.Len()+size > maxSnapshot {
			return ErrTooLarge
		}
	}
	out.Patch = b.String()
	return nil
}
