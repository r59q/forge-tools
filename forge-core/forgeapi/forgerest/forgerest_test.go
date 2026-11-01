package forgerest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// serve starts a server for h and returns a session whose API lives below
// prefix.
func serve(t *testing.T, prefix string, h http.HandlerFunc) *forgeauth.Session {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &forgeauth.Session{Kind: forgeauth.GitHub, BaseURL: srv.URL + prefix, Client: srv.Client()}
}

func TestSendJSON(t *testing.T) {
	var mu sync.Mutex
	var got struct {
		method, path, accept, ctype string
		body                        map[string]string
	}
	s := serve(t, "/api/v4", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		got.method, got.path = r.Method, r.URL.EscapedPath()
		got.accept, got.ctype = r.Header.Get("Accept"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		_, _ = w.Write([]byte(`{"id":7}`))
	})
	var v struct{ ID int }
	if err := Send(context.Background(), s, "POST", "/projects/a%2Fb/issues", map[string]string{"title": "Hi"}, &v); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if v.ID != 7 || got.method != "POST" || got.path != "/api/v4/projects/a%2Fb/issues" ||
		got.accept != "application/json" || got.ctype != "application/json" || got.body["title"] != "Hi" {
		t.Fatalf("v = %+v, request = %+v", v, got)
	}
}

func TestDoWithoutBody(t *testing.T) {
	var mu sync.Mutex
	var ctype string
	var n int64
	s := serve(t, "", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ctype, n = r.Header.Get("Content-Type"), r.ContentLength
		mu.Unlock()
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusNoContent)
	})
	h, err := Do(context.Background(), s, "DELETE", s.URL("/x"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if ctype != "" || n != 0 || h.Get("X-Test") != "yes" {
		t.Fatalf("Content-Type %q, length %d, headers %v", ctype, n, h)
	}
}

func TestDoErrors(t *testing.T) {
	s := serve(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case "/bad":
			_, _ = w.Write([]byte(`not json`))
		case "/huge":
			_, _ = w.Write([]byte(`"`))
			chunk := []byte(strings.Repeat("a", 1<<20))
			for range maxBody>>20 + 1 {
				_, _ = w.Write(chunk)
			}
			_, _ = w.Write([]byte(`"`))
		}
	})
	ctx := context.Background()
	var fe *forgehttp.Error
	if err := Send(ctx, s, "GET", "/missing", nil, nil); !errors.As(err, &fe) || !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("missing err = %v, want a not-found *forgehttp.Error", err)
	}
	var v any
	err := Send(ctx, s, "GET", "/bad?secret=hunter2", nil, &v)
	if err == nil || !strings.Contains(err.Error(), "decode") || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("bad err = %v, want a redacted decode error", err)
	}
	var str string
	if err := Send(ctx, s, "GET", "/huge", nil, &str); err == nil {
		t.Errorf("huge body decoded %d bytes, want a limit error", len(str))
	}
	if err := Send(ctx, nil, "GET", "/x", nil, nil); err == nil {
		t.Error("nil session err = nil")
	}
}

func TestDoRejectsMethodChange(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	s := serve(t, "", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
		case "/kept":
			http.Redirect(w, r, "/new", http.StatusPermanentRedirect)
		default:
			_, _ = w.Write([]byte(`{"title":"old"}`))
		}
	})
	ctx := context.Background()
	var v map[string]string
	err := Send(ctx, s, "PATCH", "/old?secret=hunter2", map[string]string{"title": "new"}, &v)
	if err == nil || !strings.Contains(err.Error(), "redirected") || strings.Contains(err.Error(), "hunter2") || v != nil {
		t.Fatalf("PATCH 301: v = %v, err = %v, want a redacted redirect error", v, err)
	}
	// A GET may follow a 301, and a 308 keeps the method.
	if err := Send(ctx, s, "GET", "/old", nil, &v); err != nil {
		t.Fatalf("GET 301: %v", err)
	}
	if err := Send(ctx, s, "PATCH", "/kept", map[string]string{"title": "new"}, &v); err != nil {
		t.Fatalf("PATCH 308: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(seen, "PATCH /new") {
		t.Errorf("requests = %v, want the 308 followed with PATCH", seen)
	}
}

// failing is a transport that always fails.
type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("boom") }

func TestDoTransportErrorDropsQuery(t *testing.T) {
	s := &forgeauth.Session{Kind: forgeauth.GitLab, BaseURL: "https://gitlab.example/api/v4", Client: &http.Client{Transport: failing{}}}
	err := Send(context.Background(), s, "GET", "/users?username=alice", nil, nil)
	if err == nil || strings.Contains(err.Error(), "alice") || !strings.Contains(err.Error(), "gitlab.example/api/v4/users") {
		t.Fatalf("err = %v, want the URL without its query", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	live := &forgeauth.Session{Kind: forgeauth.GitHub, BaseURL: srv.URL, Client: srv.Client()}
	if err := Send(ctx, live, "GET", "/x?secret=hunter2", nil, nil); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("canceled err = %v, want a redacted context.Canceled", err)
	}
}

func TestPages(t *testing.T) {
	var pages atomic.Int32
	s := serve(t, "/c/mine", func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if n < 3 {
			// Relative links resolve against the current page.
			w.Header().Set("Link", fmt.Sprintf(`<items?page=%d>; rel="next"`, max(n, 1)+1))
		}
		_, _ = fmt.Fprintf(w, `[%d, %d]`, 2*n, 2*n+1)
	})
	got, err := Pages(context.Background(), s, "/items?per_page=2", func(raw json.RawMessage) (int, error) {
		var n int
		err := json.Unmarshal(raw, &n)
		return n, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[0 1 4 5 6 7]" || pages.Load() != 3 {
		t.Fatalf("Pages = %v after %d pages", got, pages.Load())
	}
}

func TestPagesDecodeError(t *testing.T) {
	s := serve(t, "", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[1]`))
	})
	want := errors.New("boom")
	_, err := Pages(context.Background(), s, "/x", func(json.RawMessage) (int, error) { return 0, want })
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestPaginationStaysOnSource(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer other.Close()
	cases := map[string]string{
		"other host":       other.URL + "/c/mine/items",
		"other scheme":     "ftp://HOST/c/mine/items",
		"other connection": "/c/other/items",
		"prefix lookalike": "/c/mine2/items",
		"malformed":        "http://[::1",
	}
	for name, link := range cases {
		t.Run(name, func(t *testing.T) {
			s := serve(t, "/c/mine", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Link", `<`+strings.Replace(link, "HOST", r.Host, 1)+`>; rel="next"`)
				_, _ = w.Write([]byte(`[]`))
			})
			_, err := Pages(context.Background(), s, "/items", decodeAny)
			if err == nil || !strings.Contains(err.Error(), "pagination link") {
				t.Fatalf("err = %v, want a pagination error", err)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("followed a link to another host %d times", n)
	}
}

func TestPaginationLoop(t *testing.T) {
	s := serve(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `</items?per_page=100>; rel="next"`)
		_, _ = w.Write([]byte(`[]`))
	})
	if _, err := Pages(context.Background(), s, "/items?per_page=100", decodeAny); err == nil || !strings.Contains(err.Error(), "loops") {
		t.Fatalf("err = %v, want a loop error", err)
	}
}

func TestPaginationLimit(t *testing.T) {
	var hits atomic.Int32
	s := serve(t, "", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.Header().Set("Link", fmt.Sprintf(`</items?page=%d>; rel="next"`, n+1))
		_, _ = w.Write([]byte(`[]`))
	})
	if _, err := Pages(context.Background(), s, "/items", decodeAny); err == nil || !strings.Contains(err.Error(), "pages") {
		t.Fatalf("err = %v, want a page limit error", err)
	}
	if n := hits.Load(); n != maxPages {
		t.Errorf("fetched %d pages, want %d", n, maxPages)
	}
}

func TestCleanPath(t *testing.T) {
	good := map[string]string{
		"mrbean/teddy":         "mrbean/teddy",
		" /group/sub/project/": "group/sub/project",
		"a.b/c-d_e":            "a.b/c-d_e",
	}
	for in, want := range good {
		if got, ok := CleanPath(in); !ok || got != want {
			t.Errorf("CleanPath(%q) = %q, %v, want %q", in, got, ok, want)
		}
	}
	for _, p := range []string{"", "solo", "/solo/", "a//b", "a/../b", "a/./b", "a/b%2Fc", "a/b?x", "a/b#x", `a\b/c`, "a/b c", "a/b\tc", "a/b\x7f"} {
		if got, ok := CleanPath(p); ok {
			t.Errorf("CleanPath(%q) = %q, true, want false", p, got)
		}
	}
}

func decodeAny(raw json.RawMessage) (any, error) {
	var v any
	err := json.Unmarshal(raw, &v)
	return v, err
}
