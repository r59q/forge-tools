package review

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
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
		return captureComparison(ctx, s, repo, in, out)
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
