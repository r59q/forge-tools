package servicekit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBearerTokenFromEnvironment(t *testing.T) {
	t.Setenv("SERVICEKIT_TEST_TOKEN", "s3cret")

	token, err := BearerToken(t.Context(), "service", "", "SERVICEKIT_TEST_TOKEN")
	if err != nil {
		t.Fatal(err)
	}

	// Tokens are reread, so rotation needs no restart.
	t.Setenv("SERVICEKIT_TEST_TOKEN", "rotated")
	if got, err := token.Token(t.Context()); err != nil || got != "rotated" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestBearerTokenFromFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	token, err := BearerToken(t.Context(), "service", file, "UNUSED")
	if err != nil {
		t.Fatal(err)
	}

	if got, err := token.Token(t.Context()); err != nil || got != "s3cret" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestBearerTokenErrors(t *testing.T) {
	t.Setenv("SERVICEKIT_TEST_TOKEN", "two words")

	_, err := BearerToken(t.Context(), "connector", "", "SERVICEKIT_TEST_TOKEN")
	if err == nil || err.Error() != "connector token must be a nonempty bearer token" {
		t.Errorf("got %v", err)
	}

	if err != nil && strings.Contains(err.Error(), "two words") {
		t.Error("error leaks the token")
	}

	_, err = BearerToken(t.Context(), "service", "", "SERVICEKIT_TEST_UNSET")
	if err == nil || err.Error() != "service token required: configure token file or environment source" {
		t.Errorf("got %v", err)
	}
}
