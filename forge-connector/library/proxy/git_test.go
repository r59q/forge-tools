package proxy_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
)

const (
	uploadType  = "application/x-git-upload-pack-request"
	receiveType = "application/x-git-receive-pack-request"
)

// gitForge records the last request a fake Git host received.
type gitForge struct {
	srv      *httptest.Server
	hits     atomic.Int32
	received atomic.Int64
	reply    func(w http.ResponseWriter, r *http.Request)

	mu      sync.Mutex
	request *http.Request
	body    []byte
}

// last returns the last complete request and its body.
func (g *gitForge) last() (*http.Request, []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.request, g.body
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func newGitForge(t *testing.T) *gitForge {
	t.Helper()
	g := &gitForge{}
	g.srv = serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.hits.Add(1)
		body, err := io.ReadAll(countingReader{r.Body, &g.received})
		if err != nil {
			return
		}
		g.mu.Lock()
		g.request, g.body = r, body
		g.mu.Unlock()
		if g.reply != nil {
			g.reply(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = io.WriteString(w, "refs")
	}))
	return g
}

func gitSetup(t *testing.T, push bool) (*gitForge, *fakeBroker, proxy.Config) {
	t.Helper()
	g := newGitForge(t)
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: g.srv.URL + "/api/v3"}, gitBase: g.srv.URL + "/git"}
	c := config(t, b)
	c.AllowGitPush = push
	return g, b, c
}

func gitRequest(method, target, contentType string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Header.Set("Authorization", "Bearer "+clientToken)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

func TestGitForwardsAllowedRequests(t *testing.T) {
	g, b, c := gitSetup(t, true)
	h := handler(t, c)
	for _, tc := range []struct {
		method, path, contentType, upstream string
	}{
		{http.MethodGet, "/g/conn/owner/repo.git/info/refs?service=git-upload-pack", "", "/git/owner/repo.git/info/refs?service=git-upload-pack"},
		{http.MethodGet, "/g/conn/owner/repo.git/info/refs?service=git-receive-pack", "", "/git/owner/repo.git/info/refs?service=git-receive-pack"},
		{http.MethodPost, "/g/conn/group/sub/.github.git/git-upload-pack", uploadType, "/git/group/sub/.github.git/git-upload-pack"},
		{http.MethodPost, "/g/conn/o/r-1_2.x.git/git-receive-pack", receiveType + "; charset=binary", "/git/o/r-1_2.x.git/git-receive-pack"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := gitRequest(tc.method, tc.path, tc.contentType, strings.NewReader("pack data"))
			r.Header.Set("Git-Protocol", "version=2")
			r.Header.Set("Accept", "application/x-git-upload-pack-result")
			for _, name := range []string{"Cookie", "Private-Token", "Proxy-Authorization", "X-Forwarded-For", "X-Other"} {
				r.Header.Set(name, clientToken)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 || w.Body.String() != "refs" {
				t.Fatalf("status = %d, body %q", w.Code, w.Body)
			}
			got, body := g.last()
			if got.URL.RequestURI() != tc.upstream {
				t.Errorf("upstream = %s", got.URL.RequestURI())
			}
			want := http.Header{}
			github.SetGitToken(want, forgeToken)
			if got.Header.Get("Authorization") != want.Get("Authorization") {
				t.Errorf("Authorization = %q", got.Header.Get("Authorization"))
			}
			for name, values := range got.Header {
				for _, v := range values {
					if strings.Contains(v, clientToken) {
						t.Errorf("caller credential reached the forge in %s", name)
					}
				}
			}
			if got.Header.Get("Git-Protocol") != "version=2" || got.Header.Get("Accept") == "" {
				t.Error("Git-Protocol or Accept not forwarded")
			}
			if tc.method == http.MethodPost {
				if string(body) != "pack data" || !strings.HasPrefix(got.Header.Get("Content-Type"), "application/x-git-") {
					t.Errorf("body %q, type %q", body, got.Header.Get("Content-Type"))
				}
			} else if len(body) != 0 {
				t.Error("GET body forwarded")
			}
		})
	}
	if b.issuances.Load() != 4 {
		t.Errorf("issuances = %d", b.issuances.Load())
	}
}

func TestGitBasicClientAuth(t *testing.T) {
	_, b, c := gitSetup(t, false)
	h := handler(t, c)
	basic := func(user, pass string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}
	for auth, want := range map[string]int{
		basic("anyone", clientToken):      200,
		basic("", clientToken):            200,
		basic(clientToken, ""):            401,
		basic("x", "wrong"):               401,
		"Basic !!!":                       401,
		"Bearer wrong":                    401,
		"Token " + clientToken:            401,
		basic("x", clientToken+"\x00bad"): 401,
	} {
		r := httptest.NewRequest(http.MethodGet, "/g/conn/o/r.git/info/refs?service=git-upload-pack", nil)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%q: status = %d, want %d", auth, w.Code, want)
		}
		if w.Code == 401 && w.Header().Get("WWW-Authenticate") != `Basic realm="forge-connector", charset="UTF-8"` {
			t.Errorf("%q: WWW-Authenticate = %q", auth, w.Header().Get("WWW-Authenticate"))
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/g/conn/o/r.git/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	before := b.issuances.Load()
	h.ServeHTTP(w, r)
	if w.Code != 401 || b.issuances.Load() != before || w.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("missing token: status = %d", w.Code)
	}
}

func TestGitRejectsBeforeBroker(t *testing.T) {
	g, b, c := gitSetup(t, false)
	h := handler(t, c)
	for _, tc := range []struct {
		method, target, contentType string
		want                        int
	}{
		// Traversal, escapes and encoded slashes.
		{"GET", "/g/conn/../x.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/a/../../b.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/./b.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/a/...git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/%2e%2e/b.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/a%2fb.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/a%252fb.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/a%5cb.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/a/%62.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn//a.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/a/.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/a:b/c.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/conn/a@b/c.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "/g/co%6en/a/b.git/info/refs?service=git-upload-pack", "", 400},
		{"GET", "http://other.example/g/conn/a/b.git/info/refs?service=git-upload-pack", "", 400},
		// Outside the allow-list: dumb HTTP, archives, LFS and query tampering.
		{"GET", "/g/", "", 404},
		{"GET", "/g/conn", "", 404},
		{"GET", "/g/conn/a/b.git/HEAD", "", 404},
		{"GET", "/g/conn/a/b.git/objects/info/packs", "", 404},
		{"GET", "/g/conn/a/b.git/info/refs", "", 404},
		{"GET", "/g/conn/a/b.git/info/refs?", "", 404},
		{"GET", "/g/conn/a/b.git/info/refs?service=git-upload-archive", "", 404},
		{"GET", "/g/conn/a/b.git/info/refs?service=git-upload-pack&x=1", "", 404},
		{"GET", "/g/conn/a/b.git/info/refs?x=1&service=git-upload-pack", "", 404},
		{"GET", "/g/conn/a/b.git/info/refs?service=git-upload-pack&service=git-receive-pack", "", 404},
		{"GET", "/g/conn/a/b.git/info/refs?service=git%2Dupload-pack", "", 404},
		{"POST", "/g/conn/a/b.git/git-upload-archive", uploadType, 404},
		{"POST", "/g/conn/a/b.git/info/lfs/objects/batch", "application/vnd.git-lfs+json", 404},
		{"POST", "/g/conn/a/b.git/git-upload-pack?service=git-upload-pack", uploadType, 404},
		{"POST", "/g/conn/a/b.git/git-upload-pack?", uploadType, 404},
		// Push is off.
		{"GET", "/g/conn/a/b.git/info/refs?service=git-receive-pack", "", 404},
		{"POST", "/g/conn/a/b.git/git-receive-pack", receiveType, 404},
		// Methods and content types.
		{"POST", "/g/conn/a/b.git/info/refs?service=git-upload-pack", uploadType, 405},
		{"HEAD", "/g/conn/a/b.git/info/refs?service=git-upload-pack", "", 405},
		{"GET", "/g/conn/a/b.git/git-upload-pack", "", 405},
		{"PUT", "/g/conn/a/b.git/git-upload-pack", uploadType, 405},
		{"POST", "/g/conn/a/b.git/git-upload-pack", "", 415},
		{"POST", "/g/conn/a/b.git/git-upload-pack", "text/plain", 415},
		{"POST", "/g/conn/a/b.git/git-upload-pack", receiveType, 415},
		{"POST", "/g/conn/a/b.git/git-upload-pack", "application/x-git-upload-pack-request;;", 415},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			r := gitRequest(tc.method, tc.target, tc.contentType, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
	r := gitRequest("POST", "/g/conn/a/b.git/git-upload-pack", uploadType, nil)
	r.Header.Set("Content-Encoding", "br")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Errorf("Content-Encoding br: status = %d", w.Code)
	}
	r = gitRequest("POST", "/g/conn/a/b.git/git-upload-pack", uploadType, nil)
	r.Header.Add("Content-Type", uploadType)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Errorf("two content types: status = %d", w.Code)
	}
	if g.hits.Load() != 0 || b.lookups.Load() != 0 || b.verifications.Load() != 0 || b.issuances.Load() != 0 {
		t.Fatal("rejected request reached the broker or forge")
	}
}

func TestGitCleansResponses(t *testing.T) {
	g, _, c := gitSetup(t, false)
	other := newGitForge(t)
	h := handler(t, c)
	get := func() *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := gitRequest(http.MethodGet, "/g/conn/o/r.git/info/refs?service=git-upload-pack", "", nil)
		r.Host = "proxy.example"
		h.ServeHTTP(w, r)
		return w
	}
	g.reply = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "session="+forgeToken)
		w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
		w.Header().Set("Link", "<https://other.example>; rel=next")
		w.Header().Set("Alt-Svc", `h3=":443"`)
		w.Header().Set("Trailer", "X-Trailer")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "denied")
		w.Header().Set("X-Trailer", "secret")
	}
	w := get()
	if w.Code != 401 || w.Body.String() != "denied" {
		t.Fatalf("status = %d", w.Code)
	}
	for _, name := range []string{"Set-Cookie", "Link", "Alt-Svc", "Trailer", "X-Trailer"} {
		if w.Header().Get(name) != "" || w.Result().Trailer.Get(name) != "" {
			t.Errorf("%s returned to caller", name)
		}
	}
	if w.Header().Get("WWW-Authenticate") != `Basic realm="forge-connector", charset="UTF-8"` {
		t.Errorf("WWW-Authenticate = %q", w.Header().Get("WWW-Authenticate"))
	}
	for location, want := range map[string]string{
		g.srv.URL + "/git/o/moved.git/info/refs?service=git-upload-pack": "http://proxy.example/g/conn/o/moved.git/info/refs?service=git-upload-pack",
		"/git/a/b/c.git/info/refs?service=git-upload-pack":               "http://proxy.example/g/conn/a/b/c.git/info/refs?service=git-upload-pack",
		other.srv.URL + "/git/o/r.git/info/refs?service=git-upload-pack": "",
		g.srv.URL + "/login": "",
		g.srv.URL + "/o/r.git/info/refs?service=git-upload-pack":                                                 "",
		g.srv.URL + "/git/o/r.git/info/refs":                                                                     "",
		g.srv.URL + "/git/o/r.git/info/refs?service=git-receive-pack":                                            "",
		g.srv.URL + "/git/../r.git/info/refs?service=git-upload-pack":                                            "",
		"https://user:pass@" + g.srv.Listener.Addr().String() + "/git/o/r.git/info/refs?service=git-upload-pack": "",
	} {
		g.reply = func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusFound)
		}
		w := get()
		if w.Code != http.StatusFound || w.Header().Get("Location") != want {
			t.Errorf("Location %s: status %d, got %q, want %q", location, w.Code, w.Header().Get("Location"), want)
		}
	}
	if other.hits.Load() != 0 || g.hits.Load() != 10 {
		t.Fatalf("redirect followed: other %d, forge %d", other.hits.Load(), g.hits.Load())
	}
}

func TestGitUpstreamChecks(t *testing.T) {
	g := newGitForge(t)
	conn := forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: "https://forge.example/api/v3"}
	for _, issued := range []broker.IssuedToken{
		{Kind: forgeauth.GitHub, BaseURL: conn.BaseURL, Token: forgeToken},
		{Kind: forgeauth.GitHub, BaseURL: conn.BaseURL, GitBaseURL: "ftp://forge.example", Token: forgeToken},
		{Kind: forgeauth.GitHub, BaseURL: conn.BaseURL, GitBaseURL: "https://u:p@forge.example", Token: forgeToken},
		{Kind: forgeauth.GitHub, BaseURL: conn.BaseURL, GitBaseURL: "https://forge.example?x", Token: forgeToken},
		{Kind: forgeauth.GitHub, BaseURL: conn.BaseURL, GitBaseURL: "https://forge.example/a/../b", Token: forgeToken},
		{Kind: forgeauth.GitLab, BaseURL: conn.BaseURL, GitBaseURL: g.srv.URL, Token: forgeToken},
		{Kind: forgeauth.GitHub, BaseURL: "https://other.example/api/v3", GitBaseURL: g.srv.URL, Token: forgeToken},
		{Kind: forgeauth.GitHub, BaseURL: conn.BaseURL, GitBaseURL: g.srv.URL},
		{Kind: forgeauth.GitHub, BaseURL: conn.BaseURL, GitBaseURL: g.srv.URL, Token: "bad\r\ntoken"},
	} {
		b := &fakeBroker{conn: conn, issued: &issued}
		w := httptest.NewRecorder()
		handler(t, config(t, b)).ServeHTTP(w, gitRequest(http.MethodGet, "/g/conn/o/r.git/info/refs?service=git-upload-pack", "", nil))
		if w.Code != 502 {
			t.Errorf("%+v: status = %d", issued, w.Code)
		}
	}
	// A provider without Git support fails closed.
	var reg forgeauth.Registry
	if err := reg.Register(noGitProvider{github.Provider{}}); err != nil {
		t.Fatal(err)
	}
	b := &fakeBroker{conn: conn, gitBase: g.srv.URL}
	h := handler(t, proxy.Config{Verifier: b, TokenIssuer: b, Registry: &reg})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, gitRequest(http.MethodGet, "/g/conn/o/r.git/info/refs?service=git-upload-pack", "", nil))
	if w.Code != 502 || b.issuances.Load() != 0 {
		t.Errorf("provider without Git: status = %d", w.Code)
	}
	if g.hits.Load() != 0 {
		t.Fatal("forge contacted")
	}
	if _, err := proxy.New(proxy.Config{Verifier: b, TokenIssuer: b, Registry: &reg, MaxGitPushBytes: -1}); err == nil {
		t.Error("negative push cap accepted")
	}
}

// noGitProvider hides SetGitToken.
type noGitProvider struct{ forgeauth.Provider }

func TestGitBodyCaps(t *testing.T) {
	g, b, c := gitSetup(t, true)
	c.MaxGitPushBytes = 1 << 20
	h := serve(t, handler(t, c))
	post := func(path, contentType string, body io.Reader, length int64) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, h.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = length
		req.Header.Set("Authorization", "Bearer "+clientToken)
		req.Header.Set("Content-Type", contentType)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		readResponse(t, resp)
		return resp.StatusCode
	}
	big := bytes.Repeat([]byte("x"), 2<<20)
	if code := post("/g/conn/o/r.git/git-receive-pack", receiveType, bytes.NewReader(big), int64(len(big))); code != 413 {
		t.Errorf("declared length over cap: %d", code)
	}
	if g.hits.Load() != 0 || b.issuances.Load() != 0 {
		t.Error("oversized request reached the broker or forge")
	}
	// Unknown length: the cap applies while streaming.
	if code := post("/g/conn/o/r.git/git-receive-pack", receiveType, io.MultiReader(bytes.NewReader(big)), -1); code != 413 {
		t.Errorf("streamed body over cap: %d", code)
	}
	if n := g.received.Load(); n > 1<<20 {
		t.Errorf("forge received %d bytes", n)
	}
	fetch := bytes.Repeat([]byte("y"), int(proxy.MaxGitFetchBytes)+1)
	if code := post("/g/conn/o/r.git/git-upload-pack", uploadType, bytes.NewReader(fetch), int64(len(fetch))); code != 413 {
		t.Errorf("fetch over cap: %d", code)
	}
	code := post("/g/conn/o/r.git/git-receive-pack", receiveType, bytes.NewReader(big[:1<<20]), 1<<20)
	if _, body := g.last(); code != 200 || len(body) != 1<<20 {
		t.Errorf("body at cap: %d, forge got %d bytes", code, len(body))
	}
}

func TestGitStreamsRequestBody(t *testing.T) {
	first := make(chan struct{})
	forge := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1)
		if _, err := io.ReadFull(r.Body, buf); err == nil {
			close(first)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	}))
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: forge.URL + "/api/v3"}, gitBase: forge.URL}
	h := serve(t, handler(t, config(t, b)))
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write(bytes.Repeat([]byte("a"), 64<<10))
		// The forge must see data before the request body ends.
		select {
		case <-first:
			_, _ = pw.Write([]byte("tail"))
			pw.Close()
		case <-time.After(10 * time.Second):
			pw.CloseWithError(io.ErrUnexpectedEOF)
		}
	}()
	req, _ := http.NewRequest(http.MethodPost, h.URL+"/g/conn/o/r.git/git-upload-pack", pr)
	req.Header.Set("Authorization", "Bearer "+clientToken)
	req.Header.Set("Content-Type", uploadType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readResponse(t, resp)
	select {
	case <-first:
	default:
		t.Fatal("request body was buffered")
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestGitKeepsAPIRoute(t *testing.T) {
	// /c/ paths that look like Git routes still go to the API base.
	var path string
	forge := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.RequestURI()
		w.WriteHeader(204)
	}))
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: forge.URL + "/api"}, gitBase: forge.URL + "/git"}
	w := httptest.NewRecorder()
	handler(t, config(t, b)).ServeHTTP(w, gitRequest(http.MethodGet, "/c/conn/g/o/r.git/info/refs?service=git-receive-pack", "", nil))
	if w.Code != 204 || path != "/api/g/o/r.git/info/refs?service=git-receive-pack" {
		t.Errorf("status = %d, path = %q", w.Code, path)
	}
}
