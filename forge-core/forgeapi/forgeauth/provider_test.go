package forgeauth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
)

type fakeProvider struct{ kind Kind }

func (p fakeProvider) Kind() Kind { return p.kind }

func (p fakeProvider) Open(ctx context.Context, conn Connection, creds Credentials) (*Session, error) {
	return OpenSession(ctx, p.kind, conn, creds, setTestToken)
}

func (p fakeProvider) Whoami(ctx context.Context, s *Session) (Identity, error) {
	if err := CheckSession(s, p.kind); err != nil {
		return Identity{}, err
	}
	return Identity{ID: "1", Login: string(p.kind)}, nil
}

func (p fakeProvider) SetToken(h http.Header, tok string) { setTestToken(h, tok) }

func (p fakeProvider) TokenFromHeader(h http.Header) (string, bool) {
	return HeaderToken(h, testHeader)
}

func TestRegistry(t *testing.T) {
	ctx := context.Background()
	var r Registry
	if kinds := r.Kinds(); len(kinds) != 0 {
		t.Errorf("zero Registry has kinds %v", kinds)
	}
	if _, err := r.Provider(GitHub); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("Provider() error = %v, want ErrUnknownKind", err)
	}
	for _, k := range []Kind{GitLab, GitHub} {
		if err := r.Register(fakeProvider{k}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Register(fakeProvider{GitHub}); err == nil {
		t.Error("duplicate Register() succeeded")
	}
	if err := r.Register(nil); err == nil {
		t.Error("Register(nil) succeeded")
	}
	if got := r.Kinds(); !slices.Equal(got, []Kind{GitHub, GitLab}) {
		t.Errorf("Kinds() = %v", got)
	}

	s, err := r.Open(ctx, Connection{Kind: GitLab, BaseURL: "https://gitlab.example.com/api/v4"}, StaticToken("t"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind != GitLab {
		t.Errorf("session kind = %s", s.Kind)
	}
	id, err := r.Whoami(ctx, s)
	if err != nil || id.Login != "gitlab" {
		t.Errorf("Whoami() = %+v, %v", id, err)
	}
	if _, err := r.Open(ctx, Connection{Kind: Forgejo, BaseURL: "https://x"}, StaticToken("t")); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("Open(forgejo) error = %v, want ErrUnknownKind", err)
	}
	if _, err := r.Whoami(ctx, &Session{Kind: Forgejo}); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("Whoami(forgejo) error = %v, want ErrUnknownKind", err)
	}
}

func TestRegistryConcurrent(t *testing.T) {
	var r Registry
	var wg sync.WaitGroup
	for _, k := range []Kind{GitHub, GitLab, Forgejo} {
		wg.Add(2)
		go func() { defer wg.Done(); r.Register(fakeProvider{k}) }()
		go func() { defer wg.Done(); r.Provider(k); r.Kinds() }()
	}
	wg.Wait()
	if len(r.Kinds()) != 3 {
		t.Errorf("Kinds() = %v", r.Kinds())
	}
}

func TestCheckSession(t *testing.T) {
	if err := CheckSession(nil, GitHub); err == nil {
		t.Error("nil session accepted")
	}
	if err := CheckSession(&Session{Kind: GitHub}, GitHub); err == nil {
		t.Error("session without client accepted")
	}
	if err := CheckSession(&Session{Kind: GitLab, Client: http.DefaultClient}, GitHub); err == nil {
		t.Error("wrong kind accepted")
	}
	if err := CheckSession(&Session{Kind: GitHub, Client: http.DefaultClient}, GitHub); err != nil {
		t.Errorf("CheckSession() = %v", err)
	}
}

func TestAuthorizationToken(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
		wantOK bool
	}{
		{"bearer", []string{"Bearer abc"}, "abc", true},
		{"case insensitive", []string{"bEaReR abc"}, "abc", true},
		{"token scheme", []string{"token abc"}, "abc", true},
		{"extra spaces", []string{"  Bearer   abc  "}, "abc", true},
		{"missing", nil, "", false},
		{"empty", []string{""}, "", false},
		{"scheme only", []string{"Bearer"}, "", false},
		{"scheme and space", []string{"Bearer "}, "", false},
		{"other scheme", []string{"Basic YTpi"}, "", false},
		{"two parts", []string{"Bearer abc def"}, "", false},
		{"repeated", []string{"Bearer abc", "Bearer def"}, "", false},
		{"control char", []string{"Bearer a\x01b"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{"Authorization": tt.values}
			got, ok := AuthorizationToken(h, "Bearer", "token")
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("AuthorizationToken() = %q, %v, want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestHeaderToken(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
		wantOK bool
	}{
		{"set", []string{"abc"}, "abc", true},
		{"trimmed", []string{" abc "}, "abc", true},
		{"missing", nil, "", false},
		{"empty", []string{""}, "", false},
		{"space inside", []string{"a b"}, "", false},
		{"repeated", []string{"abc", "def"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for _, v := range tt.values {
				h.Add("Private-Token", v)
			}
			got, ok := HeaderToken(h, "PRIVATE-TOKEN")
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("HeaderToken() = %q, %v, want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
