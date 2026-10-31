// Package forgejo is the forgeauth provider for Forgejo.
package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Provider is the forgeauth.Provider for Forgejo. The zero value is ready to
// use.
type Provider struct{}

var _ forgeauth.Provider = Provider{}

// Kind returns forgeauth.Forgejo.
func (Provider) Kind() forgeauth.Kind { return forgeauth.Forgejo }

// Open opens a Forgejo session. conn.BaseURL is required, such as
// https://codeberg.org/api/v1; see forgeauth.DefaultBaseURL.
func (Provider) Open(ctx context.Context, conn forgeauth.Connection, creds forgeauth.Credentials) (*forgeauth.Session, error) {
	return forgeauth.OpenSession(ctx, forgeauth.Forgejo, conn, creds, SetToken)
}

// Whoami returns the authenticated user from GET /user.
func (Provider) Whoami(ctx context.Context, s *forgeauth.Session) (forgeauth.Identity, error) {
	if err := forgeauth.CheckSession(s, forgeauth.Forgejo); err != nil {
		return forgeauth.Identity{}, err
	}
	var u struct {
		ID       json.Number `json:"id"`
		Login    string      `json:"login"`
		FullName string      `json:"full_name"`
		Email    string      `json:"email"`
	}
	if err := s.GetJSON(ctx, "/user", &u); err != nil {
		return forgeauth.Identity{}, err
	}
	if u.ID == "" || u.Login == "" {
		return forgeauth.Identity{}, errors.New("forgejo: /user response has no id or login")
	}
	return forgeauth.Identity{ID: u.ID.String(), Login: u.Login, Name: u.FullName, Email: u.Email}, nil
}

// SetToken calls SetToken.
func (Provider) SetToken(h http.Header, token string) { SetToken(h, token) }

// TokenFromHeader calls TokenFromHeader.
func (Provider) TokenFromHeader(h http.Header) (string, bool) { return TokenFromHeader(h) }

// SetToken sets "Authorization: token <token>" in h.
func SetToken(h http.Header, token string) {
	h.Set("Authorization", "token "+token)
}

// TokenFromHeader reads a token from an "Authorization: token <token>" or
// "Authorization: Bearer <token>" header, the two forms Forgejo accepts.
func TokenFromHeader(h http.Header) (string, bool) {
	return forgeauth.AuthorizationToken(h, "token", "Bearer")
}
