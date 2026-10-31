package forgejo

import (
	"net/http"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/internal/providertest"
)

func TestProvider(t *testing.T) {
	providertest.Run(t, providertest.Config{
		Provider: Provider{},
		Kind:     forgeauth.Forgejo,
		APIPath:  "/api/v1",
		Header:   "Authorization",
		Prefix:   "token ",
		User:     `{"id":42,"login":"alice","login_name":"","full_name":"Alice Example","email":"alice@example.com","is_admin":false}`,
		Want:     forgeauth.Identity{ID: "42", Login: "alice", Name: "Alice Example", Email: "alice@example.com"},
	})
}

func TestTokenFromHeader(t *testing.T) {
	tests := []struct {
		auth   string
		want   string
		wantOK bool
	}{
		{"token abc123", "abc123", true},
		{"Token abc123", "abc123", true},
		{"Bearer abc123", "abc123", true},
		{"Basic eDp5", "", false},
		{"abc123", "", false},
	}
	for _, tt := range tests {
		got, ok := TokenFromHeader(http.Header{"Authorization": {tt.auth}})
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("TokenFromHeader(%q) = %q, %v, want %q, %v", tt.auth, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestOpenNeedsBaseURL(t *testing.T) {
	if _, err := (Provider{}).Open(t.Context(), forgeauth.Connection{}, forgeauth.StaticToken("t")); err == nil {
		t.Error("Open() without BaseURL succeeded")
	}
}
