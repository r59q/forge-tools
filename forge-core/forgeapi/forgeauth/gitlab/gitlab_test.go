package gitlab

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/internal/providertest"
)

func TestProvider(t *testing.T) {
	providertest.Run(t, providertest.Config{
		Provider: Provider{},
		Kind:     forgeauth.GitLab,
		APIPath:  "/api/v4",
		Header:   "Private-Token",
		User:     `{"id":1,"username":"john_smith","name":"John Smith","state":"active","email":"john@example.com","public_email":"pub@example.com"}`,
		Want:     forgeauth.Identity{ID: "1", Login: "john_smith", Name: "John Smith", Email: "john@example.com"},
	})
}

func TestTokenFromHeader(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   string
		wantOK bool
	}{
		{"private token", http.Header{"Private-Token": {"glpat-abc"}}, "glpat-abc", true},
		{"bearer", http.Header{"Authorization": {"Bearer glpat-abc"}}, "glpat-abc", true},
		{"private token wins", http.Header{"Private-Token": {"one"}, "Authorization": {"Bearer two"}}, "one", true},
		{"bad private token does not fall back", http.Header{"Private-Token": {""}, "Authorization": {"Bearer two"}}, "", false},
		{"repeated private token", http.Header{"Private-Token": {"a", "b"}}, "", false},
		{"token scheme", http.Header{"Authorization": {"token glpat-abc"}}, "", false},
	}
	for _, tt := range tests {
		got, ok := TokenFromHeader(tt.header)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("%s: TokenFromHeader() = %q, %v, want %q, %v", tt.name, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestWhoamiPublicEmail(t *testing.T) {
	// Without admin rights, GitLab may return only public_email.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":7,"username":"u","name":"U","public_email":"pub@example.com"}`)
	}))
	defer srv.Close()
	s, err := Provider{}.Open(t.Context(), forgeauth.Connection{BaseURL: srv.URL + "/api/v4"}, forgeauth.StaticToken("t"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := Provider{}.Whoami(t.Context(), s)
	if err != nil || id.Email != "pub@example.com" {
		t.Errorf("Whoami() = %+v, %v", id, err)
	}
}

func TestOpenNeedsBaseURL(t *testing.T) {
	if _, err := (Provider{}).Open(t.Context(), forgeauth.Connection{}, forgeauth.StaticToken("t")); err == nil {
		t.Error("Open() without BaseURL succeeded")
	}
}
