package forgeauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secret = "s3cret-token-value"

func TestStaticToken(t *testing.T) {
	tok, err := StaticToken(secret).Token(context.Background())
	if err != nil || tok != secret {
		t.Fatalf("Token() = %q, %v", tok, err)
	}
	if _, err := StaticToken("").Token(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Errorf("empty Token() error = %v, want ErrNoToken", err)
	}
}

func TestStaticTokenRedacted(t *testing.T) {
	tok := StaticToken(secret)
	type wrapper struct {
		Creds Credentials
		Tok   StaticToken
	}
	w := wrapper{tok, tok}
	var outs []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%10.3s"} {
		outs = append(outs, fmt.Sprintf(verb, tok), fmt.Sprintf(verb, w), fmt.Sprintf(verb, &w))
	}
	outs = append(outs, fmt.Sprint(tok), fmt.Sprintln(tok), tok.String(), tok.GoString())

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	log.Info("open", "token", tok, "creds", Credentials(tok), "w", w)
	slog.New(slog.NewTextHandler(&buf, nil)).Info("open", "token", tok)
	outs = append(outs, buf.String())

	j, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	outs = append(outs, string(j))

	for _, out := range outs {
		if strings.Contains(out, secret) || strings.Contains(out, fmt.Sprintf("%x", secret)) {
			t.Errorf("output leaks token: %s", out)
		}
		if !strings.Contains(out, redacted) {
			t.Errorf("output not redacted: %s", out)
		}
	}
}

func TestEnvToken(t *testing.T) {
	const name = "FORGEAUTH_TEST_TOKEN"
	tests := []struct {
		name    string
		set     bool
		value   string
		want    string
		wantErr string
	}{
		{"set", true, secret, secret, ""},
		{"missing", false, "", "", "is not set"},
		{"empty", true, "", "", "is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(name, tt.value)
			} else {
				t.Setenv(name, "")
				os.Unsetenv(name)
			}
			got, err := EnvToken(name).Token(context.Background())
			if tt.wantErr != "" {
				if !errors.Is(err, ErrNoToken) || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), name) {
					t.Fatalf("Token() error = %v, want ErrNoToken containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("Token() = %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestEnvTokenReadEachCall(t *testing.T) {
	const name = "FORGEAUTH_TEST_TOKEN"
	creds := EnvToken(name)
	t.Setenv(name, "one")
	if got, _ := creds.Token(context.Background()); got != "one" {
		t.Fatalf("Token() = %q, want one", got)
	}
	t.Setenv(name, "two")
	if got, _ := creds.Token(context.Background()); got != "two" {
		t.Fatalf("Token() = %q, want two", got)
	}
}

func TestFileToken(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name    string
		path    string
		want    string
		wantErr string
	}{
		{"plain", write("plain", secret), secret, ""},
		{"trimmed", write("trimmed", "\n  "+secret+" \r\n\t"), secret, ""},
		{"missing", filepath.Join(dir, "missing"), "", "no such file"},
		{"empty", write("empty", ""), "", "is empty"},
		{"whitespace only", write("blank", " \n\t\n"), "", "is empty"},
		{"too large", write("large", strings.Repeat("a", maxTokenFile+1)), "", "too large"},
		{"directory", dir, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FileToken(tt.path).Token(context.Background())
			if tt.want == "" {
				if !errors.Is(err, ErrNoToken) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Token() = %q, %v, want ErrNoToken containing %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("Token() = %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestFileTokenReadEachCall(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	creds := FileToken(p)
	for _, want := range []string{"one", "two"} {
		if err := os.WriteFile(p, []byte(want+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := creds.Token(context.Background()); err != nil || got != want {
			t.Fatalf("Token() = %q, %v, want %q", got, err, want)
		}
	}
}

func TestCheckToken(t *testing.T) {
	for _, tok := range []string{"", "a b", "a\tb", "a\r\nX-Evil: 1", "a\x00", "a\x7f"} {
		err := checkToken(tok)
		if err == nil {
			t.Errorf("checkToken(%q) = nil, want error", tok)
		} else if tok != "" && strings.Contains(err.Error(), tok) {
			t.Errorf("checkToken(%q) error contains token: %v", tok, err)
		}
	}
	for _, tok := range []string{"abc", "ghp_AbC123", "glpat-x_y.z", "a/b+c="} {
		if err := checkToken(tok); err != nil {
			t.Errorf("checkToken(%q) = %v", tok, err)
		}
	}
}
