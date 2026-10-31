// Package gitlab is the forgeauth provider for GitLab.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Provider is the forgeauth.Provider for GitLab. The zero value is ready to
// use.
type Provider struct{}

var _ forgeauth.Provider = Provider{}

// Kind returns forgeauth.GitLab.
func (Provider) Kind() forgeauth.Kind { return forgeauth.GitLab }

// Open opens a GitLab session. conn.BaseURL is required, such as
// https://gitlab.com/api/v4; see forgeauth.DefaultBaseURL.
func (Provider) Open(ctx context.Context, conn forgeauth.Connection, creds forgeauth.Credentials) (*forgeauth.Session, error) {
	return forgeauth.OpenSession(ctx, forgeauth.GitLab, conn, creds, SetToken)
}

// Whoami returns the authenticated user from GET /user.
func (Provider) Whoami(ctx context.Context, s *forgeauth.Session) (forgeauth.Identity, error) {
	if err := forgeauth.CheckSession(s, forgeauth.GitLab); err != nil {
		return forgeauth.Identity{}, err
	}
	var u struct {
		ID          json.Number `json:"id"`
		Username    string      `json:"username"`
		Name        string      `json:"name"`
		Email       string      `json:"email"`
		PublicEmail string      `json:"public_email"`
	}
	if err := s.GetJSON(ctx, "/user", &u); err != nil {
		return forgeauth.Identity{}, err
	}
	if u.ID == "" || u.Username == "" {
		return forgeauth.Identity{}, errors.New("gitlab: /user response has no id or username")
	}
	email := u.Email
	if email == "" {
		email = u.PublicEmail
	}
	return forgeauth.Identity{ID: u.ID.String(), Login: u.Username, Name: u.Name, Email: email}, nil
}

// SetToken calls SetToken.
func (Provider) SetToken(h http.Header, token string) { SetToken(h, token) }

// TokenFromHeader calls TokenFromHeader.
func (Provider) TokenFromHeader(h http.Header) (string, bool) { return TokenFromHeader(h) }

// SetToken sets "PRIVATE-TOKEN: <token>" in h.
func SetToken(h http.Header, token string) {
	h.Set("PRIVATE-TOKEN", token)
}

// TokenFromHeader reads a token from a "PRIVATE-TOKEN: <token>" header or,
// if there is none, from "Authorization: Bearer <token>". GitLab accepts
// both.
func TokenFromHeader(h http.Header) (string, bool) {
	if len(h.Values("PRIVATE-TOKEN")) > 0 {
		return forgeauth.HeaderToken(h, "PRIVATE-TOKEN")
	}
	return forgeauth.AuthorizationToken(h, "Bearer")
}
