package servicekit

import (
	"context"
	"fmt"
	"net/http"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Token is a bearer token read from a file or an environment variable. It
// is reread on each use, so rotation needs no restart.
type Token struct {
	file, env string
}

// Token returns the current token. Errors never contain it.
func (t Token) Token(ctx context.Context) (string, error) {
	if t.file != "" {
		return forgeauth.FileToken(t.file).Token(ctx)
	}

	return forgeauth.EnvToken(t.env).Token(ctx)
}

// BearerToken returns the token in file, or in the env variable when file
// is empty. It checks that the token is set and usable as a bearer token.
// Errors name the token by name and never contain its value.
func BearerToken(ctx context.Context, name, file, env string) (Token, error) {
	t := Token{file: file, env: env}
	token, err := t.Token(ctx)
	if err != nil {
		return Token{}, fmt.Errorf("%s token required: configure token file or environment source", name)
	}

	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	if parsed, ok := forgeauth.AuthorizationToken(h, "Bearer"); !ok || parsed != token {
		return Token{}, fmt.Errorf("%s token must be a nonempty bearer token", name)
	}

	return t, nil
}
