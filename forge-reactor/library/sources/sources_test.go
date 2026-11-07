package sources

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

// forge serves the hooks API of GitHub and Forgejo.
type forge struct {
	mu     sync.Mutex
	hooks  map[string]hookBody // by path, such as /repos/team/app/hooks/1
	next   int64
	refuse map[string]bool // repositories that refuse every request
}

func (f *forge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	repo := strings.Join(strings.Split(path, "/")[2:4], "/")
	if f.refuse[repo] {
		http.Error(w, "forbidden", http.StatusForbidden)

		return
	}

	switch r.Method {
	case http.MethodPost:
		f.create(w, r, path)
	case http.MethodPatch:
		f.edit(w, r, path)
	case http.MethodDelete:
		if _, ok := f.hooks[path]; !ok {
			http.NotFound(w, r)

			return
		}
		delete(f.hooks, path)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *forge) create(w http.ResponseWriter, r *http.Request, path string) {
	var b hookBody
	_ = json.NewDecoder(r.Body).Decode(&b)
	f.next++
	f.hooks[path+"/"+strconv.FormatInt(f.next, 10)] = b
	_ = json.NewEncoder(w).Encode(map[string]int64{"id": f.next})
}

func (f *forge) edit(w http.ResponseWriter, r *http.Request, path string) {
	old, ok := f.hooks[path]
	if !ok {
		http.NotFound(w, r)

		return
	}
	var b hookBody
	_ = json.NewDecoder(r.Body).Decode(&b)
	old.Config = b.Config
	f.hooks[path] = old
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

func (f *forge) hook(path string) (hookBody, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.hooks[path]

	return b, ok
}

func (f *forge) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.hooks)
}

func setup(t *testing.T) (*Manager, *forge) {
	t.Helper()
	f := &forge{hooks: map[string]hookBody{}, refuse: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	sessions := func(_ context.Context, connection string) (*forgeauth.Session, error) {
		switch connection {
		case "gh":
			return &forgeauth.Session{Kind: forgeauth.GitHub, BaseURL: srv.URL, Client: srv.Client()}, nil
		case "fj":
			return &forgeauth.Session{Kind: forgeauth.Forgejo, BaseURL: srv.URL + "/api/v1", Client: srv.Client()}, nil
		}

		return nil, reactor.ErrNotFound
	}
	m, err := New(&Manager{
		Store:     &MemoryStore{},
		Sessions:  sessions,
		PublicURL: "https://example.org/reactor/",
		Reserved:  []string{"configured"},
	})
	if err != nil {
		t.Fatal(err)
	}

	return m, f
}

func TestCreateWithoutRegistration(t *testing.T) {
	m, f := setup(t)
	ctx := context.Background()

	src, err := m.Create(ctx, Request{ID: "app", Forge: forgeauth.GitHub, Connection: "gh", Repositories: []string{"team/app", "Team/App"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Secret) != 64 || src.Registered() || len(src.Repositories) != 1 || f.count() != 0 {
		t.Fatalf("source = %+v, hooks = %d", src, f.count())
	}

	got, err := m.Get(ctx, "app")
	if err != nil || got.Secret != src.Secret || string(got.Webhook().Secret) != src.Secret {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if m.URL("app") != "https://example.org/reactor/webhooks/app" {
		t.Fatalf("url = %q", m.URL("app"))
	}
}

func TestCreateRegisters(t *testing.T) {
	for _, tc := range []struct {
		connection, path, field string
		forge                   forgeauth.Kind
	}{
		{"gh", "/repos/team/app/hooks/1", "name", forgeauth.GitHub},
		{"fj", "/repos/team/app/hooks/1", "type", forgeauth.Forgejo},
	} {
		t.Run(string(tc.forge), func(t *testing.T) {
			m, f := setup(t)
			src, err := m.Create(context.Background(), Request{
				ID: "app", Forge: tc.forge, Connection: tc.connection, Repositories: []string{"team/app"}, Register: true,
			})
			if err != nil {
				t.Fatal(err)
			}

			hook, ok := f.hook(tc.path)
			if !ok || !src.Registered() || src.Hooks["team/app"] != 1 {
				t.Fatalf("source = %+v, hook = %v", src, ok)
			}
			if hook.Config["secret"] != src.Secret || hook.Config["url"] != "https://example.org/reactor/webhooks/app" ||
				hook.Config["content_type"] != "json" || !hook.Active || len(hook.Events) != 2 {
				t.Fatalf("hook = %+v", hook)
			}
			if tc.forge == forgeauth.GitHub && hook.Name != "web" || tc.forge == forgeauth.Forgejo && hook.Type != "forgejo" {
				t.Fatalf("hook = %+v", hook)
			}
		})
	}
}

func TestCreateRollsBack(t *testing.T) {
	m, f := setup(t)
	f.refuse["team/lib"] = true

	_, err := m.Create(context.Background(), Request{
		ID: "app", Forge: forgeauth.GitHub, Connection: "gh", Repositories: []string{"team/app", "team/lib"}, Register: true,
	})
	if !errors.Is(err, ErrRegistration) {
		t.Fatalf("err = %v", err)
	}
	if f.count() != 0 {
		t.Fatalf("hooks left = %d", f.count())
	}
	if _, err := m.Get(context.Background(), "app"); !errors.Is(err, reactor.ErrNotFound) {
		t.Fatalf("source kept: %v", err)
	}
}

func TestCreateRejects(t *testing.T) {
	m, _ := setup(t)
	ctx := context.Background()
	if _, err := m.Create(ctx, Request{ID: "app", Forge: forgeauth.GitHub}); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		req  Request
		want error
	}{
		"duplicate":      {Request{ID: "app", Forge: forgeauth.GitHub}, reactor.ErrExists},
		"reserved":       {Request{ID: "configured", Forge: forgeauth.GitHub}, reactor.ErrInvalid},
		"bad id":         {Request{ID: "App!", Forge: forgeauth.GitHub}, reactor.ErrInvalid},
		"gitlab":         {Request{ID: "x", Forge: forgeauth.GitLab}, reactor.ErrInvalid},
		"bad repository": {Request{ID: "x", Forge: forgeauth.GitHub, Repositories: []string{"team/../x"}}, reactor.ErrInvalid},
		"no repository":  {Request{ID: "x", Forge: forgeauth.GitHub, Connection: "gh", Register: true}, reactor.ErrInvalid},
		"wrong forge":    {Request{ID: "x", Forge: forgeauth.Forgejo, Connection: "gh", Repositories: []string{"a/b"}, Register: true}, reactor.ErrInvalid},
		"unknown":        {Request{ID: "x", Forge: forgeauth.GitHub, Connection: "zz", Repositories: []string{"a/b"}, Register: true}, reactor.ErrNotFound},
	} {
		if _, err := m.Create(ctx, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestRegistrationNeedsConfiguration(t *testing.T) {
	m, err := New(&Manager{Store: &MemoryStore{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Create(context.Background(), Request{ID: "x", Forge: forgeauth.GitHub, Connection: "gh", Repositories: []string{"a/b"}, Register: true})
	if !errors.Is(err, ErrNotConfigured) || m.URL("x") != "" {
		t.Fatalf("err = %v, url = %q", err, m.URL("x"))
	}
	if _, err := New(&Manager{Store: &MemoryStore{}, PublicURL: "ftp://example.org"}); !errors.Is(err, reactor.ErrInvalid) {
		t.Fatalf("bad public URL: %v", err)
	}
}

func TestRotate(t *testing.T) {
	m, f := setup(t)
	ctx := context.Background()
	src, err := m.Create(ctx, Request{ID: "app", Forge: forgeauth.Forgejo, Connection: "fj", Repositories: []string{"team/app", "team/lib"}, Register: true})
	if err != nil {
		t.Fatal(err)
	}

	f.refuse["team/lib"] = true
	rotated, failed, err := m.Rotate(ctx, "app")
	if err != nil || rotated.Secret == src.Secret || len(failed) != 1 || failed[0] != "team/lib" {
		t.Fatalf("rotated = %+v, failed = %v, err = %v", rotated, failed, err)
	}
	hook, _ := f.hook("/repos/team/app/hooks/1")
	if hook.Config["secret"] != rotated.Secret {
		t.Fatal("hook keeps the old secret")
	}
	if stored, _ := m.Get(ctx, "app"); stored.Secret != rotated.Secret {
		t.Fatal("store keeps the old secret")
	}
	if _, _, err := m.Rotate(ctx, "missing"); !errors.Is(err, reactor.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestDelete(t *testing.T) {
	m, f := setup(t)
	ctx := context.Background()
	_, err := m.Create(ctx, Request{ID: "app", Forge: forgeauth.GitHub, Connection: "gh", Repositories: []string{"team/app", "team/lib"}, Register: true})
	if err != nil {
		t.Fatal(err)
	}

	f.refuse["team/lib"] = true
	failed, err := m.Delete(ctx, "app")
	if err != nil || len(failed) != 1 || failed[0] != "team/lib" || f.count() != 1 {
		t.Fatalf("failed = %v, err = %v, hooks = %d", failed, err, f.count())
	}
	if list, _ := m.List(ctx); len(list) != 0 {
		t.Fatalf("list = %+v", list)
	}
	if _, err := m.Delete(ctx, "app"); !errors.Is(err, reactor.ErrNotFound) {
		t.Fatalf("again: %v", err)
	}
}

func TestStorePersists(t *testing.T) {
	var saved []byte
	save := func(_ context.Context, b []byte) error { saved = b; return nil }
	m, err := New(&Manager{Store: &MemoryStore{Save: save}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(context.Background(), Request{ID: "b", Forge: forgeauth.GitHub}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(context.Background(), Request{ID: "a", Forge: forgeauth.Forgejo}); err != nil {
		t.Fatal(err)
	}

	store, err := NewMemoryStore(saved, nil)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, _ := New(&Manager{Store: store})
	list, err := reloaded.List(context.Background())
	if err != nil || len(list) != 2 || list[0].ID != "a" || list[1].Secret == "" {
		t.Fatalf("list = %+v, %v", list, err)
	}

	failing := &MemoryStore{Save: func(context.Context, []byte) error { return errors.New("disk full") }}
	m, _ = New(&Manager{Store: failing})
	if _, err := m.Create(context.Background(), Request{ID: "a", Forge: forgeauth.GitHub}); err == nil {
		t.Fatal("failed save accepted")
	}
	if list, _ := m.List(context.Background()); len(list) != 0 {
		t.Fatalf("failed save visible: %+v", list)
	}
}
