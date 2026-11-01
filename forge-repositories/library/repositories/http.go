package repositories

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

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// maxPages caps how many pages one list call follows.
const maxPages = 1000

// maxBody caps the size of one response body.
const maxBody = 32 << 20

// rawJSON is one undecoded JSON value from a forge response.
type rawJSON = json.RawMessage

// send sends a request for path on s and decodes the JSON response into v.
// A non-nil body is sent as JSON.
func send(ctx context.Context, s *source, method, path string, body any, v any) error {
	_, err := do(ctx, s, method, s.Session.URL(path), body, v)
	return err
}

// do sends one request and returns the response headers.
func do(ctx context.Context, s *source, method, target string, body any, v any) (http.Header, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.Session.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := forgehttp.CheckResponse(resp); err != nil {
		return nil, err
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v); err != nil {
		return nil, fmt.Errorf("decode %s: %w", forgehttp.RedactURL(req.URL), err)
	}
	return resp.Header, nil
}

// getPages reads a JSON array from path and every following page, and
// decodes each element with decode. It follows Link headers only within
// the session's origin, so a forge cannot redirect the listing elsewhere.
func getPages[T any](ctx context.Context, s *source, path string, decode func(rawJSON) (T, error)) ([]T, error) {
	base, err := url.Parse(s.Session.BaseURL)
	if err != nil {
		return nil, err
	}
	var out []T
	seen := map[string]bool{}
	next := s.Session.URL(path)
	for page := 0; next != ""; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("more than %d pages", maxPages)
		}
		seen[next] = true
		var items []rawJSON
		h, err := do(ctx, s, http.MethodGet, next, nil, &items)
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
