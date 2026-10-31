package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

func TestRunPages(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, `{"message":"bad credentials"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/user":
			fmt.Fprint(w, `{"id":1,"login":"octocat"}`)
		case r.URL.Path == "/user/repos" && r.URL.Query().Get("page") == "":
			w.Header().Set("Link", fmt.Sprintf(`<%s/user/repos?page=2>; rel="next"`, srv.URL))
			fmt.Fprint(w, `[{"full_name":"octocat/one"}]`)
		case r.URL.Path == "/user/repos":
			fmt.Fprint(w, `[{"full_name":"octocat/two"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out strings.Builder
	conn := forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: srv.URL}
	if err := run(context.Background(), conn, forgeauth.StaticToken("secret"), &out); err != nil {
		t.Fatal(err)
	}
	want := "Signed in as octocat\noctocat/one\noctocat/two\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestRunRejectedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"bad credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	conn := forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: srv.URL}
	if err := run(context.Background(), conn, forgeauth.StaticToken("wrong"), &strings.Builder{}); err == nil {
		t.Fatal("run succeeded with a rejected token")
	}
}
