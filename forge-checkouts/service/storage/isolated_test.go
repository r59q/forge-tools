package storage

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/r59q/forge-tools/forge-checkouts/library/checkouts"
	"github.com/r59q/forge-tools/forge-checkouts/service/internal/testrepo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

func TestIsolatedCheckoutSurvivesRestart(t *testing.T) {
	ctx := t.Context()
	remote := httptest.NewServer(testrepo.HTTP(t, testrepo.New(t), "/"))
	defer remote.Close()
	root := t.TempDir()
	store, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	m, err := checkouts.New(ctx, root, store, func(context.Context, string) (*forgeauth.GitRemote, error) {
		return &forgeauth.GitRemote{BaseURL: remote.URL, Client: remote.Client()}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	w, err := m.Create(ctx, checkouts.Input{Repository: checkouts.Repository{Source: "fixture", Path: "owner/repo"}, Mode: checkouts.Isolated})
	if err != nil {
		t.Fatal(err)
	}
	w, err = m.Retain(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	testrepo.Git(t, w.Location, "switch", "-c", "consumer")
	testrepo.Git(t, w.Location, "commit", "--allow-empty", "-m", "consumer")
	wantRevision := testrepo.Git(t, w.Location, "rev-parse", "HEAD")
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	next, err := checkouts.New(ctx, root, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	got, err := next.Get(ctx, w.ID)
	if err != nil || got != w {
		t.Fatal(got, err)
	}
	all, err := next.List(ctx)
	if err != nil || len(all) != 1 || all[0] != w {
		t.Fatal(all, err)
	}
	st, err := next.Inspect(ctx, w.ID)
	if err != nil || st.Revision != wantRevision || st.Branch != "consumer" {
		t.Fatal(st, err)
	}
	state, err := store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cache := state.Checkouts[w.ID].GitDirectory
	if testrepo.Git(t, cache, "rev-parse", "refs/checkouts/"+w.ID) != w.Revision {
		t.Fatal("lost pin")
	}
	if err = next.Remove(ctx, w.ID, false); err != nil {
		t.Fatal(err)
	}
	if testrepo.Git(t, cache, "for-each-ref", "refs/checkouts/") != "" {
		t.Fatal("pin survived removal")
	}
}
