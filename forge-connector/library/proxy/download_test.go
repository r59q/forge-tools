package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

func TestDownloadRedirects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		location string
		method   string
		hosts    []string
		download bool
	}{
		{"disabled", "https://store.example.net/file?sig=signed", "GET", nil, false},
		{"allowed", "https://store.example.net/file?sig=signed", "GET", []string{"store.example.net"}, true},
		{"subdomain", "https://a.storage.example.net/file", "GET", []string{"*.storage.example.net"}, true},
		{"suffix attack", "https://evil-storage.example.net/file", "GET", []string{"*.storage.example.net"}, false},
		{"apex", "https://storage.example.net/file", "GET", []string{"*.storage.example.net"}, false},
		{"http", "http://store.example.net/file", "GET", []string{"store.example.net"}, false},
		{"userinfo", "https://user@store.example.net/file", "GET", []string{"store.example.net"}, false},
		{"port", "https://store.example.net:444/file", "GET", []string{"store.example.net"}, false},
		{"write", "https://store.example.net/file", "POST", []string{"store.example.net"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: "https://forge.example/api"}}
			c := config(t, b)
			c.DownloadHosts = tc.hosts
			calls := 0
			c.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if r.Header.Get("Authorization") != "Bearer "+forgeToken {
						t.Fatal("missing forge credential")
					}

					return &http.Response{
						StatusCode: 302,
						Header:     http.Header{"Location": {tc.location}},
						Body:       io.NopCloser(strings.NewReader("redirect")),
						Request:    r,
					}, nil
				}

				if !tc.download {
					t.Fatal("contacted blocked host")
				}
				if httptrace.ContextClientTrace(r.Context()) != nil {
					t.Error("forwarded proxy response trace")
				}
				if len(r.Header) != 0 || r.Method != "GET" || r.Body != nil {
					t.Fatalf("forwarded caller data: %+v", r.Header)
				}

				return &http.Response{
					StatusCode: 200,
					Header:     http.Header{"Content-Type": {"text/plain"}, "Set-Cookie": {"secret=cookie"}},
					Body:       io.NopCloser(strings.NewReader("log bytes")),
					Request:    r,
				}, nil
			})
			h := handler(t, c)
			r := httptest.NewRequest(tc.method, "/c/conn/logs?original=private", nil)
			r.Header.Set("Authorization", "Bearer "+clientToken)
			r.Header.Set("Cookie", "caller=secret")
			r.Header.Set("X-Private", "secret")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if tc.download {
				if calls != 2 || w.Code != 200 || w.Body.String() != "log bytes" {
					t.Fatalf("download: %d %d %s", calls, w.Code, w.Body)
				}
			} else if calls != 1 || w.Code != 302 {
				t.Fatalf("blocked: %d %d", calls, w.Code)
			}

			if w.Header().Get("Location") != "" || w.Header().Get("Set-Cookie") != "" {
				t.Fatal("leaked signed URL or cookie")
			}
		})
	}
}

func TestDownloadRedirectChainAndValidation(t *testing.T) {
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: "https://forge.example/api"}}
	for _, host := range []string{"*", "*.com", "127.0.0.1", "https://example.com", "example.com/path", "a..com", "UPPER.example", "*."} {
		c := config(t, b)
		c.DownloadHosts = []string{host}
		if _, err := proxy.New(c); err == nil {
			t.Errorf("accepted %q", host)
		}
	}

	c := config(t, b)
	c.DownloadHosts = []string{"store.example.net"}
	calls := 0
	c.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		target := "https://store.example.net/file?sig=private"
		if calls > 1 {
			target = "http://127.0.0.1/private"
		}

		return &http.Response{
			StatusCode: 302,
			Header:     http.Header{"Location": {target}},
			Body:       io.NopCloser(strings.NewReader("private")),
			Request:    r,
		}, nil
	})
	r := httptest.NewRequest("GET", "/c/conn/logs", nil)
	r.Header.Set("Authorization", "Bearer "+clientToken)
	w := httptest.NewRecorder()
	handler(t, c).ServeHTTP(w, r)
	if w.Code != 502 || calls != 2 || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("chain %d %d %s", w.Code, calls, w.Body)
	}
}
