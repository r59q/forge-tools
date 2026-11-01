// Package forgerest sends JSON requests to forge REST APIs over a
// forgeauth.Session. Tools use it to call GitHub, GitLab and Forgejo
// endpoints without handling tokens.
//
//	var repo struct{ FullName string `json:"full_name"` }
//	err := forgerest.Send(ctx, s, "GET", "/repos/mrbean/teddy", nil, &repo)
//
// Failed responses are *forgehttp.Error values. URLs in errors drop the
// query string. A redirect that changes the method is an error, so a write
// is never dropped silently.
package forgerest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// maxPages caps how many pages one Pages call follows.
const maxPages = 1000

// maxBody caps the size of one decoded response body.
const maxBody = 32 << 20

// Send sends a request for path on s and decodes the JSON response into v.
// See Do.
func Send(ctx context.Context, s *forgeauth.Session, method, path string, body, v any) error {
	_, err := Do(ctx, s, method, path, body, v)
	return err
}

// Do sends one request and returns the response headers. target is a path
// or an absolute URL, resolved with Session.URL. A non-nil body is sent as
// JSON. The response is decoded into v, up to 32 MiB; a nil v skips
// decoding, such as for 204 responses. A 4xx or 5xx response is a
// *forgehttp.Error. A redirect that changes the method, as 301, 302 and 303
// do for a write, is an error.
func Do(ctx context.Context, s *forgeauth.Session, method, target string, body, v any) (http.Header, error) {
	if s == nil || s.Client == nil {
		return nil, errors.New("forgerest: no open session")
	}
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.URL(target), r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			ue.URL = forgehttp.RedactURL(req.URL)
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.Request != nil && resp.Request.Method != req.Method {
		return nil, fmt.Errorf("forgerest: %s %s was redirected to %s %s", req.Method,
			forgehttp.RedactURL(req.URL), resp.Request.Method, forgehttp.RedactURL(resp.Request.URL))
	}
	if err := forgehttp.CheckResponse(resp); err != nil {
		return nil, err
	}
	if v != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v); err != nil {
			return nil, fmt.Errorf("decode %s: %w", forgehttp.RedactURL(req.URL), err)
		}
	}
	return resp.Header, nil
}

// Pages reads a JSON array from path and every following page, and decodes
// each element with decode. It follows Link "next" headers up to 1000
// pages, only within the session's scheme, host and base path, so a forge
// cannot point the listing elsewhere. A link that repeats is an error.
func Pages[T any](ctx context.Context, s *forgeauth.Session, path string, decode func(json.RawMessage) (T, error)) ([]T, error) {
	if s == nil {
		return nil, errors.New("forgerest: no open session")
	}
	base, err := url.Parse(s.BaseURL)
	if err != nil {
		return nil, err
	}
	var out []T
	seen := map[string]bool{}
	next := s.URL(path)
	for page := 0; next != ""; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("more than %d pages", maxPages)
		}
		seen[next] = true
		var items []json.RawMessage
		h, err := Do(ctx, s, http.MethodGet, next, nil, &items)
		if err != nil {
			return nil, err
		}
		for _, raw := range items {
			v, err := decode(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		if next, err = nextPage(h, next, base); err != nil {
			return nil, err
		}
		if seen[next] {
			return nil, errors.New("pagination loops")
		}
	}
	return out, nil
}

// nextPage returns the absolute "next" link in h, resolved against the
// current page URL, or "" on the last page.
func nextPage(h http.Header, current string, base *url.URL) (string, error) {
	link, ok := forgehttp.NextPage(h)
	if !ok || link == "" {
		return "", nil
	}
	cur, err := url.Parse(current)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(link)
	if err != nil {
		return "", errors.New("malformed pagination link")
	}
	u := cur.ResolveReference(ref)
	if !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) || !within(u.Path, base.Path) {
		return "", errors.New("pagination link leaves the source")
	}
	return u.String(), nil
}

// within reports whether path is root or below it. A proxy connection's
// links must stay below its /c/{connectionID} prefix.
func within(path, root string) bool {
	root = strings.TrimRight(root, "/")
	return root == "" || path == root || strings.HasPrefix(path, root+"/")
}

// CleanPath checks a repository path, such as "owner/name" or the nested
// GitLab path "group/sub/project", and trims surrounding space and slashes.
// It needs an owner and a name, and rejects empty, "." and ".." segments,
// control characters, spaces and any of % ? # \, so a path is never
// double-encoded or escapes its API route.
func CleanPath(path string) (string, bool) {
	p := strings.Trim(strings.TrimSpace(path), "/")
	segments := strings.Split(p, "/")
	if len(segments) < 2 {
		return "", false
	}
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, `%?#\`) {
			return "", false
		}
		for _, r := range seg {
			if r <= ' ' || r == 0x7f {
				return "", false
			}
		}
	}
	return p, true
}
