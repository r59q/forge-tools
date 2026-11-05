package poll

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

// forge serves branch heads like Forgejo's API.
type forge struct {
	mu    sync.Mutex
	heads map[string]string
	fail  bool
}

func (f *forge) set(path, commit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[path] = commit
}

func (f *forge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)

		return
	}
	commit, ok := f.heads[r.URL.EscapedPath()]
	if !ok {
		http.NotFound(w, r)

		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"name": "x", "commit": map[string]string{"id": commit}})
}

func setup(t *testing.T, store reactor.Store) (*Poller, *forge, *reactor.Reactor) {
	t.Helper()
	f := &forge{heads: map[string]string{
		"/api/v1/repos/team/app/branches/main":      "c1",
		"/api/v1/repos/team/app/branches/release/1": "r1",
	}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	r, err := reactor.New(reactor.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSubscription(context.Background(), reactor.Subscription{ID: "all"}); err != nil && !errors.Is(err, reactor.ErrExists) {
		t.Fatal(err)
	}

	p := &Poller{
		Reactor: r,
		Sessions: func(_ context.Context, connection string) (*forgeauth.Session, error) {
			if connection != "fj" {
				return nil, errors.New("unknown connection")
			}

			return &forgeauth.Session{Kind: forgeauth.Forgejo, BaseURL: srv.URL + "/api/v1", Client: srv.Client()}, nil
		},
		Targets: []Target{{ID: "app", Connection: "fj", Repository: "team/app", Branches: []string{"main", "release/1"}}},
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}

	return p, f, r
}

func events(t *testing.T, r *reactor.Reactor) []reactor.Event {
	t.Helper()
	es, _, err := r.Events(context.Background(), reactor.EventQuery{})
	if err != nil {
		t.Fatal(err)
	}

	return es
}

func TestOnceSetsBaselineThenReportsChanges(t *testing.T) {
	p, f, r := setup(t, &reactor.JSONStore{})
	ctx := context.Background()

	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if es := events(t, r); len(es) != 0 {
		t.Fatalf("baseline and unchanged rounds emitted %+v", es)
	}

	f.set("/api/v1/repos/team/app/branches/release/1", "r2")
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}
	es := events(t, r)
	if len(es) != 1 {
		t.Fatalf("events = %+v", es)
	}
	e := es[0]
	var data reactor.BranchUpdate
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Fatal(err)
	}
	if e.Origin != reactor.OriginPoll || e.Source != "app" || e.Forge != forgeauth.Forgejo || e.Connection != "fj" ||
		e.Repository != "team/app" || e.Subject != "refs/heads/release/1" || data.Before != "r1" || data.After != "r2" {
		t.Errorf("event = %+v, data %+v", e, data)
	}
}

func TestFailedReadKeepsBaseline(t *testing.T) {
	p, f, r := setup(t, &reactor.JSONStore{})
	ctx := context.Background()
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}

	var reported []string
	p.OnError = func(_ Target, branch string, _ error) { reported = append(reported, branch) }
	f.fail = true
	if err := p.Once(ctx); err == nil {
		t.Fatal("Once() hid a failed read")
	}
	if len(reported) != 2 || len(events(t, r)) != 0 {
		t.Fatalf("reported %v, events %v", reported, events(t, r))
	}

	// A missing branch is a failed read, not a change.
	f.fail = false
	f.set("/api/v1/repos/team/app/branches/main", "c2")
	delete(f.heads, "/api/v1/repos/team/app/branches/release/1")
	if err := p.Once(ctx); err == nil {
		t.Fatal("missing branch was not reported")
	}
	es := events(t, r)
	if len(es) != 1 || es[0].Subject != "refs/heads/main" {
		t.Fatalf("events = %+v", es)
	}

	f.set("/api/v1/repos/team/app/branches/release/1", "r1")
	if err := p.Once(ctx); err != nil || len(events(t, r)) != 1 {
		t.Fatalf("restored branch emitted a change: %v", err)
	}
}

func TestBaselineSurvivesRestart(t *testing.T) {
	var disk []byte
	open := func() reactor.Store {
		store, err := reactor.NewJSONStore(disk, func(_ context.Context, data []byte) error {
			disk = append([]byte(nil), data...)

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		return store
	}
	ctx := context.Background()
	p, f, _ := setup(t, open())
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}

	// After a restart, an unchanged head emits nothing and a change made
	// while the service was down is reported against the stored baseline.
	restarted := func() (*Poller, *reactor.Reactor) {
		r, err := reactor.New(reactor.Config{Store: open()})
		if err != nil {
			t.Fatal(err)
		}
		next := *p
		next.Reactor = r

		return &next, r
	}
	p2, r := restarted()
	if err := p2.Once(ctx); err != nil || len(events(t, r)) != 0 {
		t.Fatalf("unchanged after restart: %v, %+v", err, events(t, r))
	}

	f.set("/api/v1/repos/team/app/branches/main", "c9")
	p3, r := restarted()
	if err := p3.Once(ctx); err != nil {
		t.Fatal(err)
	}
	es := events(t, r)
	var data reactor.BranchUpdate
	if len(es) != 1 || json.Unmarshal(es[0].Data, &data) != nil || data.Before != "c1" || data.After != "c9" {
		t.Fatalf("events after restart = %+v", es)
	}
}

func TestUnsupportedConnection(t *testing.T) {
	p, _, _ := setup(t, &reactor.JSONStore{})
	p.Sessions = func(context.Context, string) (*forgeauth.Session, error) {
		return &forgeauth.Session{Kind: forgeauth.GitHub, BaseURL: "https://api.github.com"}, nil
	}
	if err := p.Once(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Once() = %v, want ErrUnsupported", err)
	}
}

func TestValidate(t *testing.T) {
	r, _ := reactor.New(reactor.Config{Store: &reactor.JSONStore{}})
	sessions := func(context.Context, string) (*forgeauth.Session, error) { return nil, nil }
	for name, targets := range map[string][]Target{
		"bad id":        {{ID: "Bad", Connection: "fj", Repository: "o/r", Branches: []string{"main"}}},
		"duplicate":     {{ID: "a", Connection: "fj", Repository: "o/r", Branches: []string{"main"}}, {ID: "a", Connection: "fj", Repository: "o/r", Branches: []string{"x"}}},
		"no connection": {{ID: "a", Repository: "o/r", Branches: []string{"main"}}},
		"bad repo":      {{ID: "a", Connection: "fj", Repository: "o/r/x", Branches: []string{"main"}}},
		"no branches":   {{ID: "a", Connection: "fj", Repository: "o/r"}},
		"bad branch":    {{ID: "a", Connection: "fj", Repository: "o/r", Branches: []string{"../main"}}},
	} {
		p := &Poller{Reactor: r, Sessions: sessions, Targets: targets}
		if err := p.Validate(); !errors.Is(err, reactor.ErrInvalid) {
			t.Errorf("%s: Validate() = %v", name, err)
		}
	}
}
