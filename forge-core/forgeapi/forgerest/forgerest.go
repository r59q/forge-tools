// Package forgerest sends JSON requests to forge REST APIs over a
// forgeauth.Session. Tools use it to call GitHub, GitLab and Forgejo
// endpoints without handling tokens.
//
//	var repo struct{ FullName string `json:"full_name"` }
//	err := forgerest.Send(ctx, s, "GET", "/repos/mrbean/teddy", nil, &repo)
//
// Failed responses are *forgehttp.Error values. URLs in errors drop the
// query string. A redirect that changes the method is an error, so a write
// is never dropped silently. Open streams a response body, and Each and
// Pages read paged lists.
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

// maxPages caps how many pages one Each or Pages call follows.
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
// each element with decode. It follows Link "next" headers like Each.
func Pages[T any](ctx context.Context, s *forgeauth.Session, path string, decode func(json.RawMessage) (T, error)) ([]T, error) {
	var out []T
	err := Each(ctx, s, path, func(raw json.RawMessage) (bool, error) {
		v, err := decode(raw)
		if err != nil {
			return false, err
		}
		out = append(out, v)
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Each reads a JSON array from path and every following page, and calls fn
// for each element in order. It stops without error when fn returns
// more = false, and fetches no further pages. It follows Link "next"
// headers up to 1000 pages, only within the session's scheme, host and
// base path, so a forge cannot point the listing elsewhere. A link that
// repeats is an error.
func Each(ctx context.Context, s *forgeauth.Session, path string, fn func(json.RawMessage) (more bool, err error)) error {
	if s == nil {
		return errors.New("forgerest: no open session")
	}
	base, err := url.Parse(s.BaseURL)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	next := s.URL(path)
	for page := 0; next != ""; page++ {
		if page == maxPages {
			return fmt.Errorf("more than %d pages", maxPages)
		}
		seen[next] = true
		var items []json.RawMessage
		h, err := Do(ctx, s, http.MethodGet, next, nil, &items)
		if err != nil {
			return err
		}
		for _, raw := range items {
			more, err := fn(raw)
			if err != nil {
				return err
			}
			if !more {
				return nil
			}
		}
		if next, err = nextPage(h, next, base); err != nil {
			return err
		}
		if seen[next] {
			return errors.New("pagination loops")
		}
	}
	return nil
}

// Open sends one request and returns the response for the caller to read
// and close, such as to stream a file. target is a path or an absolute
// URL, resolved with Session.URL, and h holds extra request headers. A 4xx
// or 5xx response is a *forgehttp.Error, and its body is closed.
//
// Open follows redirects only within the session's scheme, host and base
// path, and never one that changes the method, so a forge cannot send the
// request elsewhere. URLs in errors drop the query string.
func Open(ctx context.Context, s *forgeauth.Session, method, target string, h http.Header) (*http.Response, error) {
	if s == nil || s.Client == nil {
		return nil, errors.New("forgerest: no open session")
	}
	base, err := url.Parse(s.BaseURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, s.URL(target), nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range h {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	c := *s.Client
	c.CheckRedirect = stayOnBase(base, s.Client.CheckRedirect)
	resp, err := c.Do(req)
	if err != nil {
		// The client has closed any redirect response.
		var ue *url.Error
		if errors.As(err, &ue) {
			ue.URL = forgehttp.RedactURL(req.URL)
		}
		return nil, err
	}
	if err := forgehttp.CheckResponse(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

// maxRedirects matches net/http's default limit.
const maxRedirects = 10

// stayOnBase returns a CheckRedirect policy that refuses redirects outside
// base or that change the method, then applies next, if any.
func stayOnBase(base *url.URL, next func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		switch {
		case len(via) >= maxRedirects:
			return fmt.Errorf("forgerest: stopped after %d redirects", maxRedirects)
		case req.Method != via[0].Method:
			return fmt.Errorf("forgerest: %s %s was redirected to %s %s", via[0].Method,
				forgehttp.RedactURL(via[0].URL), req.Method, forgehttp.RedactURL(req.URL))
		case !onBase(req.URL, base):
			return fmt.Errorf("forgerest: redirect to %s leaves the source", forgehttp.RedactURL(req.URL))
		}
		if next != nil {
			return next(req, via)
		}
		return nil
	}
}

// onBase reports whether u has base's scheme and host and lies within its
// path. Paths with "." or ".." segments, such as an encoded %2e%2e, are
// refused, since a server may resolve them above base.
func onBase(u, base *url.URL) bool {
	if !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) || u.User != nil {
		return false
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return within(u.Path, base.Path)
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
	if !onBase(u, base) {
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
