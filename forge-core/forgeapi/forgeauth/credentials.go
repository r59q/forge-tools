package forgeauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// ErrNoToken is wrapped by credential errors when no token is available.
var ErrNoToken = errors.New("forgeauth: no token")

// redacted replaces secrets in formatted output.
const redacted = "[REDACTED]"

// Credentials supply the token for a session. Sessions call Token for each
// request, so a token can change while a session is open.
type Credentials interface {
	// Token returns the current token. Errors must not contain the token.
	Token(ctx context.Context) (string, error)
}

// StaticToken is a fixed token.
//
// Formatting a StaticToken with fmt, log/slog or encoding/json prints
// [REDACTED], not the token.
type StaticToken string

// Token returns the token, or an error wrapping ErrNoToken if it is empty.
func (t StaticToken) Token(context.Context) (string, error) {
	if t == "" {
		return "", fmt.Errorf("%w: static token is empty", ErrNoToken)
	}
	return string(t), nil
}

// String returns [REDACTED].
func (t StaticToken) String() string { return redacted }

// GoString returns [REDACTED].
func (t StaticToken) GoString() string { return redacted }

// Format writes [REDACTED] for every fmt verb.
func (t StaticToken) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// LogValue returns [REDACTED] for log/slog.
func (t StaticToken) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText returns [REDACTED], so encoding/json and similar never write
// the token.
func (t StaticToken) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// EnvToken is the name of an environment variable that holds a token. The
// variable is read on each call to Token.
type EnvToken string

// Token reads the environment variable. It returns an error wrapping
// ErrNoToken if the variable is unset or empty.
func (e EnvToken) Token(context.Context) (string, error) {
	v, ok := os.LookupEnv(string(e))
	if !ok {
		return "", fmt.Errorf("%w: environment variable %s is not set", ErrNoToken, string(e))
	}
	if v == "" {
		return "", fmt.Errorf("%w: environment variable %s is empty", ErrNoToken, string(e))
	}
	return v, nil
}

// FileToken is the path of a file that holds a token. The file is read on
// each call to Token, and surrounding whitespace is trimmed.
type FileToken string

// maxTokenFile caps how much of a token file is read.
const maxTokenFile = 64 << 10

// Token reads the file. It returns an error wrapping ErrNoToken if the file
// is missing, unreadable or holds only whitespace.
func (p FileToken) Token(context.Context) (string, error) {
	f, err := os.Open(string(p))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoToken, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTokenFile+1))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoToken, err)
	}
	if len(data) > maxTokenFile {
		return "", fmt.Errorf("%w: token file %s is too large", ErrNoToken, string(p))
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("%w: token file %s is empty", ErrNoToken, string(p))
	}
	return tok, nil
}

// checkToken rejects tokens that cannot be sent in a header. The error does
// not contain the token.
func checkToken(tok string) error {
	if tok == "" {
		return ErrNoToken
	}
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; c <= ' ' || c == 0x7f {
			return errors.New("forgeauth: token contains whitespace or control characters")
		}
	}
	return nil
}
