// Package github is the forgeauth provider for GitHub and GitHub Enterprise
// Server.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Provider is the forgeauth.Provider for GitHub. The zero value is ready to
// use.
type Provider struct{}

var _ forgeauth.Provider = Provider{}

// Kind returns forgeauth.GitHub.
func (Provider) Kind() forgeauth.Kind { return forgeauth.GitHub }

// Open opens a GitHub session. An empty conn.BaseURL means
// https://api.github.com.
func (Provider) Open(ctx context.Context, conn forgeauth.Connection, creds forgeauth.Credentials) (*forgeauth.Session, error) {
	return forgeauth.OpenSession(ctx, forgeauth.GitHub, conn, creds, SetToken)
}

// Whoami returns the authenticated user from GET /user.
func (Provider) Whoami(ctx context.Context, s *forgeauth.Session) (forgeauth.Identity, error) {
	if err := forgeauth.CheckSession(s, forgeauth.GitHub); err != nil {
		return forgeauth.Identity{}, err
	}
	var u struct {
		ID    json.Number `json:"id"`
		Login string      `json:"login"`
		Name  string      `json:"name"`
		Email string      `json:"email"`
	}
	if err := s.GetJSON(ctx, "/user", &u); err != nil {
		return forgeauth.Identity{}, err
	}
	if u.ID == "" || u.Login == "" {
		return forgeauth.Identity{}, errors.New("github: /user response has no id or login")
	}
	return forgeauth.Identity{ID: u.ID.String(), Login: u.Login, Name: u.Name, Email: u.Email}, nil
}

// SetToken calls SetToken.
func (Provider) SetToken(h http.Header, token string) { SetToken(h, token) }

// TokenFromHeader calls TokenFromHeader.
func (Provider) TokenFromHeader(h http.Header) (string, bool) { return TokenFromHeader(h) }

// SetToken sets "Authorization: Bearer <token>" in h.
func SetToken(h http.Header, token string) {
	h.Set("Authorization", "Bearer "+token)
}

// TokenFromHeader reads a token from an "Authorization: Bearer <token>" or
// "Authorization: token <token>" header, the two forms GitHub accepts.
func TokenFromHeader(h http.Header) (string, bool) {
	return forgeauth.AuthorizationToken(h, "Bearer", "token")
}
