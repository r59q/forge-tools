package github

import (
	"net/http"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/internal/providertest"
)

func TestProvider(t *testing.T) {
	providertest.Run(t, providertest.Config{
		Provider: Provider{},
		Kind:     forgeauth.GitHub,
		APIPath:  "/api/v3",
		Header:   "Authorization",
		Prefix:   "Bearer ",
		User:     `{"login":"octocat","id":583231,"node_id":"MDQ6VXNlcjU4MzIzMQ==","name":"The Octocat","email":null}`,
		Want:     forgeauth.Identity{ID: "583231", Login: "octocat", Name: "The Octocat"},
	})
}

func TestTokenFromHeader(t *testing.T) {
	tests := []struct {
		auth   string
		want   string
		wantOK bool
	}{
		{"Bearer ghp_abc", "ghp_abc", true},
		{"token ghp_abc", "ghp_abc", true},
		{"BEARER ghp_abc", "ghp_abc", true},
		{"Basic eDp5", "", false},
		{"ghp_abc", "", false},
	}
	for _, tt := range tests {
		got, ok := TokenFromHeader(http.Header{"Authorization": {tt.auth}})
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("TokenFromHeader(%q) = %q, %v, want %q, %v", tt.auth, got, ok, tt.want, tt.wantOK)
		}
	}
	if _, ok := TokenFromHeader(http.Header{"Private-Token": {"abc"}}); ok {
		t.Error("TokenFromHeader accepted PRIVATE-TOKEN")
	}
}

func TestDefaultBaseURL(t *testing.T) {
	s, err := Provider{}.Open(t.Context(), forgeauth.Connection{}, forgeauth.StaticToken("t"))
	if err != nil {
		t.Fatal(err)
	}
	if s.BaseURL != "https://api.github.com" {
		t.Errorf("BaseURL = %q", s.BaseURL)
	}
}
