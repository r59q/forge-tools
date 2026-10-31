package proxy_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

func TestRejectsUnsafeRequests(t *testing.T) {
	var contacted atomic.Int32
	forge := serve(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted.Add(1) }))
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: forge.URL + "/api/v1"}}
	h := handler(t, config(t, b))
	for _, path := range []string{
		"http://other.example/c/conn/user", "https://other.example/c/conn/user",
		"//other.example/c/conn/user", "/c/conn/http://other.example/user",
		"/c/conn/https://other.example/user", "/c/conn//other.example/user",
		"/c/conn/../user", "/c/conn/a/../../user", "/c/conn/./user",
		"/c/conn/%2e%2e/user", "/c/conn/%2E%2E/user", "/c/conn/.%2e/user",
		"/c/conn/%2e./user", "/c/conn/%252e%252e/user", "/c/conn/%25252e%25252e/user",
		"/c/conn/a%2f..%2f..%2fuser", "/c/conn/%2f..%2fuser", "/c/conn/%252f..%252fuser",
		"/c/conn/a\\..\\user", "/c/conn/%5c..%5cuser", "/c/conn/%255c..%255cuser",
		"/c/conn/%00user", "/c/conn/%0d%0auser", "/c/conn/%25invalid",
		"/c/conn/%09user", "/c/conn/%1fuser", "/c/conn/%7fuser",
		"/c/co%6en/user",
	} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Header.Set("Authorization", "Bearer "+clientToken)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Errorf("status = %d, want 400", w.Code)
			}
		})
	}
	for _, path := range []string{"/", "/user", "/c/", "/c/conn", "/c//user"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 404 {
			t.Errorf("%s: status = %d, want 404", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodConnect, "/c/conn/user", nil))
	if w.Code != 400 {
		t.Errorf("CONNECT status = %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/c/conn/user", nil)
	r.URL.Fragment = "fragment"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Errorf("fragment status = %d", w.Code)
	}
	if contacted.Load() != 0 || b.lookups.Load() != 0 || b.issuances.Load() != 0 {
		t.Fatal("unsafe request reached broker or forge")
	}
}

func TestPreservesSafeAPIPaths(t *testing.T) {
	var path string
	forge := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		w.WriteHeader(204)
	}))
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: forge.URL + "/api"}}
	h := handler(t, config(t, b))
	for _, suffix := range []string{"", "user", "projects/a%2Fb", "projects/a%252Fb", "repos/a/b/git/refs/heads/a:b", "repos/a/b/contents/.gitignore", "repos/a/b/contents/a..b"} {
		r := httptest.NewRequest(http.MethodGet, "/c/conn/"+suffix, nil)
		r.Header.Set("Authorization", "Bearer "+clientToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 || path != "/api/"+suffix {
			t.Errorf("%s: status = %d, path = %q", suffix, w.Code, path)
		}
	}
}

func TestInvalidEndpointAndIssuedToken(t *testing.T) {
	for _, base := range []string{
		"", "file:///api", "http://:80/api", "//forge.example/api", "https://user:secret@forge.example/api",
		"https://forge.example/api?query", "https://forge.example/api?", "https://forge.example/api#fragment",
		"https://forge.example/api/../private", "https://forge.example/api/%252e%252e/private",
	} {
		t.Run(base, func(t *testing.T) {
			b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: base}}
			r := httptest.NewRequest(http.MethodGet, "/c/conn/user", nil)
			r.Header.Set("Authorization", "Bearer "+clientToken)
			w := httptest.NewRecorder()
			handler(t, config(t, b)).ServeHTTP(w, r)
			if w.Code != 502 || b.verifications.Load() != 0 || b.issuances.Load() != 0 {
				t.Errorf("invalid endpoint: status = %d", w.Code)
			}
		})
	}
	for _, issued := range []broker.IssuedToken{
		{Kind: forgeauth.GitLab, BaseURL: "https://forge.example/api", Token: forgeToken},
		{Kind: forgeauth.GitHub, BaseURL: "https://other.example/api", Token: forgeToken},
		{Kind: forgeauth.GitHub, BaseURL: "http://forge.example/api", Token: forgeToken},
		{Kind: forgeauth.GitHub, BaseURL: "https://forge.example/other", Token: forgeToken},
		{Kind: forgeauth.GitHub, BaseURL: "https://forge.example/api"},
		{Kind: forgeauth.GitHub, BaseURL: "https://forge.example/api", Token: "unsafe\r\ntoken"},
	} {
		b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: "https://forge.example/api"}, issued: &issued}
		c := config(t, b)
		c.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Error("inconsistent credential reached transport")
			return nil, broker.ErrUnavailable
		})
		r := httptest.NewRequest(http.MethodGet, "/c/conn/user", nil)
		r.Header.Set("Authorization", "Bearer "+clientToken)
		w := httptest.NewRecorder()
		handler(t, c).ServeHTTP(w, r)
		if w.Code != 502 {
			t.Errorf("inconsistent credential: status = %d", w.Code)
		}
	}
}

func TestRedirectsAndLinksStayWithinConnection(t *testing.T) {
	var otherRequests atomic.Int32
	other := serve(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { otherRequests.Add(1) }))
	var requests atomic.Int32
	var forge *httptest.Server
	forge = serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		location := r.URL.Query().Get("location")
		if location == "same" {
			location = forge.URL + "/api/v1/users?page=2"
		}
		w.Header().Set("Location", location)
		w.Header().Add("Link", fmt.Sprintf(`<%s/api/v1/users?page=2>; rel="next"; title="a,b", </api/v1/users?page=1>; rel="first"`, forge.URL))
		w.Header().Add("Link", `<?page=3>; rel="last", <`+other.URL+`/steal>; rel="external"`)
		w.Header().Add("Link", `<`+forge.URL+`/api/v10/outside>; rel="outside", </api/v1/%252e%252e/escape>; rel="unsafe"`)
		w.Header().Add("Link", `<`+forge.URL+`/api/v1>; rel="root"`)
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(w, "forge redirect body")
	}))
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: forge.URL + "/api/v1"}}
	server := serve(t, handler(t, config(t, b)))
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, tc := range []struct{ location, want string }{
		{"same", server.URL + "/c/conn/users?page=2"},
		{"/api/v1/users?page=2", server.URL + "/c/conn/users?page=2"},
		{"users?page=2", server.URL + "/c/conn/users?page=2"},
		{"?page=2", server.URL + "/c/conn/user?page=2"},
		{other.URL + "/steal", ""},
		{"//" + strings.TrimPrefix(other.URL, "http://") + "/steal", ""},
		{strings.Replace(forge.URL, "http://", "https://", 1) + "/api/v1/users", ""},
		{forge.URL + "/api/v10/outside", ""},
		{"/outside", ""}, {"../outside", ""}, {"/api/v1/%2e%2e/outside", ""},
		{"http://user:secret@" + strings.TrimPrefix(forge.URL, "http://") + "/api/v1/user", ""},
		{"https://bad.example/%zz", ""}, {"javascript:alert(1)", ""},
	} {
		t.Run(tc.location, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, server.URL+"/c/conn/user?location="+url.QueryEscape(tc.location), nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+clientToken)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if body := readResponse(t, resp); resp.StatusCode != 307 || body != "forge redirect body" {
				t.Errorf("forge response changed: %d %q", resp.StatusCode, body)
			}
			if got := resp.Header.Get("Location"); got != tc.want {
				t.Errorf("Location = %q, want %q", got, tc.want)
			}
			links := forgehttp.ParseLinks(resp.Header)
			if len(links) != 4 {
				t.Fatalf("Link count = %d, want 4", len(links))
			}
			if got, _ := links.Get("next"); got != server.URL+"/c/conn/users?page=2" || links[0].Params["title"] != "a,b" {
				t.Error("Link URL or parameters changed")
			}
			if got, _ := links.Get("root"); got != server.URL+"/c/conn/" {
				t.Errorf("base Link = %q", got)
			}
		})
	}
	if otherRequests.Load() != 0 || requests.Load() != 14 {
		t.Fatal("proxy followed a redirect")
	}
}

func TestPagingWithForgeauthSession(t *testing.T) {
	for _, provider := range providers {
		t.Run(string(provider.Kind()), func(t *testing.T) {
			var requests atomic.Int32
			var forge *httptest.Server
			forge = serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page := requests.Add(1)
				if token, ok := provider.TokenFromHeader(r.Header); !ok || token != forgeToken {
					t.Error("wrong forge credential during pagination")
				}
				if r.URL.Path != "/api/v1/issues" || (page > 1 && r.URL.Query().Get("page") != fmt.Sprint(page)) {
					t.Errorf("unexpected page URL: %s", r.URL)
				}
				switch page {
				case 1:
					w.Header().Set("Link", `<`+forge.URL+`/api/v1/issues?page=2>; rel="next"`)
				case 2:
					w.Header().Set("Link", `<?page=3>; rel="next"`)
				}
				_, _ = fmt.Fprintf(w, `{"page":%d}`, page)
			}))
			b := &fakeBroker{conn: forgeauth.Connection{Kind: provider.Kind(), BaseURL: forge.URL + "/api/v1"}}
			server := serve(t, handler(t, config(t, b)))
			session, err := provider.Open(context.Background(), forgeauth.Connection{BaseURL: server.URL + "/c/conn"}, forgeauth.StaticToken(clientToken))
			if err != nil {
				t.Fatal(err)
			}
			next := "/issues"
			for page := 1; page <= 3; page++ {
				req, err := session.NewRequest(context.Background(), http.MethodGet, next, nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := session.Client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				if body := readResponse(t, resp); resp.StatusCode != 200 || body != fmt.Sprintf(`{"page":%d}`, page) {
					t.Fatalf("page response = %d %q", resp.StatusCode, body)
				}
				var more bool
				next, more = forgehttp.NextPage(resp.Header)
				if more != (page < 3) || (more && !strings.HasPrefix(next, server.URL+"/c/conn/")) {
					t.Fatalf("next page = %q, more = %v", next, more)
				}
			}
			if requests.Load() != 3 || b.verifications.Load() != 3 || b.issuances.Load() != 3 {
				t.Error("pagination did not stay behind authorization")
			}
		})
	}
}

func TestStreamingAndTrailerRewriting(t *testing.T) {
	release := make(chan struct{}, 1)
	defer close(release)
	var forge *httptest.Server
	forge = serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "Link, Location, X-Checksum")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "last")
		w.Header().Set("Link", `<`+forge.URL+`/api/next>; rel="next"`)
		w.Header().Set("Location", "https://other.example/steal")
		w.Header().Set("X-Checksum", "kept")
	}))
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: forge.URL + "/api"}}
	server := serve(t, handler(t, config(t, b)))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/c/conn/stream", nil)
	req.Header.Set("Authorization", "Bearer "+clientToken)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, first); err != nil || string(first) != "first" {
		t.Fatalf("first chunk = %q, error = %v", first, err)
	}
	release <- struct{}{}
	if rest := readResponse(t, resp); rest != "last" {
		t.Fatalf("remaining body = %q", rest)
	}
	if next, _ := forgehttp.NextPage(resp.Trailer); next != server.URL+"/c/conn/next" {
		t.Errorf("trailer Link = %q", next)
	}
	if resp.Trailer.Get("Location") != "" || resp.Trailer.Get("X-Checksum") != "kept" {
		t.Error("unsafe or missing trailer")
	}
}

func TestInformationalHeadersAreRewritten(t *testing.T) {
	forge := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `</api/next>; rel="next"`)
		w.Header().Set("Location", "https://other.example/steal")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Del("Link")
		w.Header().Del("Location")
		w.WriteHeader(http.StatusNoContent)
	}))
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: forge.URL + "/api"}}
	server := serve(t, handler(t, config(t, b)))
	var hints atomic.Int32
	trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, h textproto.MIMEHeader) error {
		hints.Add(1)
		if next, _ := forgehttp.NextPage(http.Header(h)); code != 103 || next != server.URL+"/c/conn/next" || h.Get("Location") != "" {
			t.Error("informational headers bypassed rewriting")
		}
		return nil
	}}
	req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, server.URL+"/c/conn/user", nil)
	req.Header.Set("Authorization", "Bearer "+clientToken)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readResponse(t, resp)
	if resp.StatusCode != 204 || hints.Load() != 1 {
		t.Error("informational response missing")
	}
}

func TestStreamsRequestAndDropsTrailers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstRead := make(chan struct{}, 1)
	forge := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first := make([]byte, 5)
		if _, err := io.ReadFull(r.Body, first); err != nil || string(first) != "first" {
			t.Error("request prefix was not streamed")
			return
		}
		firstRead <- struct{}{}
		if rest, err := io.ReadAll(r.Body); err != nil || string(rest) != "last" {
			t.Error("request suffix changed")
		}
		if len(r.Trailer) != 0 {
			t.Error("caller trailers reached forge")
		}
		w.WriteHeader(204)
	}))
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: forge.URL + "/api"}}
	server := serve(t, handler(t, config(t, b)))
	reader, writer := io.Pipe()
	defer reader.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/c/conn/upload", reader)
	req.Header.Set("Authorization", "Bearer "+clientToken)
	req.Trailer = http.Header{"Cookie": {"session=" + clientToken}, "Private-Token": {clientToken}}
	writeErr := make(chan error, 1)
	go func() {
		defer writer.Close()
		if _, err := io.WriteString(writer, "first"); err != nil {
			writeErr <- err
			return
		}
		select {
		case <-firstRead:
			_, err := io.WriteString(writer, "last")
			writeErr <- err
		case <-ctx.Done():
			writeErr <- ctx.Err()
		}
	}()
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readResponse(t, resp)
	if err := <-writeErr; err != nil || resp.StatusCode != 204 {
		t.Fatalf("streaming request: status = %d, error = %v", resp.StatusCode, err)
	}
}

func TestStreamsKnownLengthResponse(t *testing.T) {
	release := make(chan struct{}, 1)
	defer close(release)
	forge := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "9")
		_, _ = io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = io.WriteString(w, "last")
		case <-r.Context().Done():
		}
	}))
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: forge.URL}}
	server := serve(t, handler(t, config(t, b)))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/c/conn/stream", nil)
	req.Header.Set("Authorization", "Bearer "+clientToken)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, first); err != nil || string(first) != "first" {
		t.Fatalf("first response chunk = %q, error = %v", first, err)
	}
	release <- struct{}{}
	if rest := readResponse(t, resp); rest != "last" || resp.ContentLength != 9 {
		t.Errorf("remaining body = %q, length = %d", rest, resp.ContentLength)
	}
}
