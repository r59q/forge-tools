package proxy_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

type fakeLister struct {
	connections []broker.ClientConnection
	err         error
}

func (l fakeLister) ClientConnections(_ context.Context, token forgeauth.StaticToken) ([]broker.ClientConnection, error) {
	if string(token) != clientToken {
		return nil, broker.ErrUnauthorized
	}
	return l.connections, l.err
}

func TestListConnections(t *testing.T) {
	c := config(t, &fakeBroker{})
	c.Connections = fakeLister{connections: []broker.ClientConnection{
		{ID: "a", Name: "Work", Kind: forgeauth.GitLab, Status: broker.StatusOK},
		{ID: "b", Kind: forgeauth.GitHub, Status: broker.StatusRevoked},
	}}
	empty := config(t, &fakeBroker{})
	empty.Connections = fakeLister{}
	for server, want := range map[string]string{
		serve(t, handler(t, c)).URL:     `{"connections":[{"id":"a","name":"Work","kind":"gitlab","status":"ok"},{"id":"b","kind":"github","status":"revoked"}]}`,
		serve(t, handler(t, empty)).URL: `{"connections":[]}`,
	} {
		resp, err := http.DefaultClient.Do(request(t, "GET", server+"/connections", "Bearer "+clientToken))
		if err != nil {
			t.Fatal(err)
		}
		if body := readResponse(t, resp); resp.StatusCode != 200 || strings.TrimSpace(body) != want ||
			resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("GET /connections: %d %q", resp.StatusCode, body)
		}
	}
}

func TestConnectionsErrors(t *testing.T) {
	c := config(t, &fakeBroker{})
	c.Connections = fakeLister{err: broker.ErrUnavailable}
	s := serve(t, handler(t, c))
	disabled := serve(t, handler(t, config(t, &fakeBroker{})))
	for _, tc := range []struct {
		server       string
		method, auth string
		want         int
	}{
		{s.URL, "GET", "", http.StatusUnauthorized},
		{s.URL, "GET", "Basic " + clientToken, http.StatusUnauthorized},
		{s.URL, "GET", "Bearer wrong", http.StatusUnauthorized},
		{s.URL, "POST", "Bearer " + clientToken, http.StatusMethodNotAllowed},
		{s.URL, "GET", "Bearer " + clientToken, http.StatusServiceUnavailable},
		{disabled.URL, "GET", "Bearer " + clientToken, http.StatusNotFound},
	} {
		resp, err := http.DefaultClient.Do(request(t, tc.method, tc.server+"/connections", tc.auth))
		if err != nil {
			t.Fatal(err)
		}
		if body := readResponse(t, resp); resp.StatusCode != tc.want || strings.Contains(body, clientToken) {
			t.Errorf("%s %q: %d %q, want %d", tc.method, tc.auth, resp.StatusCode, body, tc.want)
		}
	}
}

func request(t *testing.T, method, url, auth string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return req
}
