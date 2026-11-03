// Package storetest provides the shared broker.Store contract tests.
package storetest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// missing is an ID no test stores.
const missing broker.ID = "missing"

// contractRun holds the state shared by the ordered contract steps.
type contractRun struct {
	t      *testing.T
	ctx    context.Context
	s      broker.Store
	forge  broker.Forge
	conn   broker.Connection
	client broker.Client
}

func contract(t *testing.T, s broker.Store) {
	forge := broker.Forge{
		ID:         "f",
		Kind:       forgeauth.GitHub,
		BaseURL:    "https://example.com/api/v3",
		GitBaseURL: "https://example.com",
	}
	conn := broker.Connection{ID: "c", ForgeID: forge.ID, Status: broker.StatusOK}

	r := &contractRun{
		t:      t,
		ctx:    context.Background(),
		s:      s,
		forge:  forge,
		conn:   conn,
		client: broker.Client{ID: "client", TokenHash: [32]byte{1}, Connections: []broker.ID{conn.ID}},
	}

	r.forges()
	r.connections()
	r.credentials()
	r.clients()
	r.deleteConnection()
	r.allConnectionsClient()
	r.deletes()
}

func (r *contractRun) wantErr(err, want error) {
	r.t.Helper()
	if !errors.Is(err, want) {
		r.t.Fatalf("error = %v, want %v", err, want)
	}
}

// forges checks forge creation, references and updates.
func (r *contractRun) forges() {
	ctx, s, f := r.ctx, r.s, &r.forge

	r.wantErr(s.CreateForge(ctx, broker.Forge{}), broker.ErrInvalid)
	r.wantErr(s.CreateConnection(ctx, r.conn, []byte("ciphertext")), broker.ErrNotFound)
	r.wantErr(s.CreateClient(ctx, r.client), broker.ErrNotFound)
	r.wantErr(s.CreateForge(ctx, *f), nil)
	r.wantErr(s.CreateForge(ctx, *f), broker.ErrConflict)

	f.Name = "renamed"
	r.wantErr(s.UpdateForge(ctx, *f), nil)
	if got, err := s.GetForge(ctx, f.ID); err != nil || got != *f {
		r.t.Fatalf("GetForge = %v, %v", got, err)
	}
	if got, err := s.ListForges(ctx); err != nil || len(got) != 1 || got[0] != *f {
		r.t.Fatalf("ListForges = %v, %v", got, err)
	}
}

// connections checks that connected forges keep their endpoints.
func (r *contractRun) connections() {
	ctx, s, f := r.ctx, r.s, r.forge

	sealed := []byte("ciphertext")
	r.wantErr(s.CreateConnection(ctx, r.conn, sealed), nil)
	sealed[0] = 'X'
	r.wantErr(s.CreateConnection(ctx, r.conn, sealed), broker.ErrConflict)
	r.wantErr(s.DeleteForge(ctx, f.ID), broker.ErrConflict)

	changed := f
	changed.BaseURL = "https://another.example.com"
	r.wantErr(s.UpdateForge(ctx, changed), broker.ErrConflict)
	changed = f
	changed.Kind = forgeauth.GitLab
	r.wantErr(s.UpdateForge(ctx, changed), broker.ErrConflict)
	changed = f
	changed.GitBaseURL = "https://git.another.example.com"
	r.wantErr(s.UpdateForge(ctx, changed), broker.ErrConflict)
	if got, err := s.GetForge(ctx, f.ID); err != nil || got != f {
		r.t.Fatalf("rejected update changed the forge: %v, %v", got, err)
	}

	r.forge.Name = "safe rename"
	r.wantErr(s.UpdateForge(ctx, r.forge), nil)
}

// credentials checks that sealed credentials are copied and updated safely.
func (r *contractRun) credentials() {
	ctx, s, c := r.ctx, r.s, &r.conn

	gotSealed, err := s.GetCredential(ctx, c.ID)
	if err != nil || string(gotSealed) != "ciphertext" {
		r.t.Fatal("CreateConnection retained caller's ciphertext slice")
	}

	gotSealed[0] = 'X'
	c.Name = "renamed"
	r.wantErr(s.UpdateConnection(ctx, *c, nil), nil)
	gotSealed, err = s.GetCredential(ctx, c.ID)
	if err != nil || string(gotSealed) != "ciphertext" {
		r.t.Fatal("GetCredential exposed stored slice or nil update lost ciphertext")
	}

	got, err := s.GetConnection(ctx, c.ID)
	if err != nil || got != *c {
		r.t.Fatalf("GetConnection = %v, %v", got, err)
	}
	if got, err := s.ListConnections(ctx); err != nil || len(got) != 1 || got[0] != *c {
		r.t.Fatalf("ListConnections = %v, %v", got, err)
	}

	changedConn := *c
	changedConn.ForgeID = "other"
	r.wantErr(s.UpdateConnection(ctx, changedConn, nil), broker.ErrInvalid)
	r.wantErr(s.UpdateConnection(ctx, *c, []byte{}), broker.ErrInvalid)

	replacement := []byte("replacement")
	r.wantErr(s.UpdateConnection(ctx, *c, replacement), nil)
	replacement[0] = 'X'
	gotSealed, err = s.GetCredential(ctx, c.ID)
	if err != nil || !bytes.Equal(gotSealed, []byte("replacement")) {
		r.t.Fatal("UpdateConnection retained caller's slice")
	}
}

// clients checks that client scopes are copied in and out.
func (r *contractRun) clients() {
	ctx, s, c := r.ctx, r.s, r.conn

	r.wantErr(s.CreateClient(ctx, r.client), nil)
	r.wantErr(s.CreateClient(ctx, r.client), broker.ErrConflict)
	r.client.Connections[0] = missing
	storedClient, err := s.GetClient(ctx, r.client.ID)
	if err != nil || !slices.Equal(storedClient.Connections, []broker.ID{c.ID}) {
		r.t.Fatal("CreateClient retained caller's scope slice")
	}

	storedClient.Name = "updated"
	r.wantErr(s.UpdateClient(ctx, storedClient), nil)
	storedClient.Connections[0] = missing
	clients, err := s.ListClients(ctx)
	if err != nil || len(clients) != 1 || clients[0].Name != "updated" || clients[0].Connections[0] != c.ID {
		r.t.Fatal("UpdateClient retained caller's scope slice")
	}

	clients[0].Connections[0] = missing
	storedClient, err = s.GetClient(ctx, r.client.ID)
	if err != nil || storedClient.Connections[0] != c.ID {
		r.t.Fatal("ListClients exposed stored scope")
	}

	storedClient.Connections[0] = missing
	r.wantErr(s.UpdateClient(ctx, storedClient), broker.ErrNotFound)
}

// deleteConnection checks that deleting a connection removes it everywhere.
func (r *contractRun) deleteConnection() {
	ctx, s, c := r.ctx, r.s, r.conn

	r.wantErr(s.DeleteConnection(ctx, c.ID), nil)
	storedClient, err := s.GetClient(ctx, r.client.ID)
	if err != nil || len(storedClient.Connections) != 0 {
		r.t.Fatal("DeleteConnection did not remove client scope")
	}

	_, err = s.GetCredential(ctx, c.ID)
	r.wantErr(err, broker.ErrNotFound)
	_, err = s.GetConnection(ctx, c.ID)
	r.wantErr(err, broker.ErrNotFound)
	r.wantErr(s.DeleteConnection(ctx, c.ID), broker.ErrNotFound)
	r.wantErr(s.UpdateConnection(ctx, c, nil), broker.ErrNotFound)
}

// allConnectionsClient checks clients granted every connection.
func (r *contractRun) allConnectionsClient() {
	ctx, s := r.ctx, r.s

	all := broker.Client{ID: "all", TokenHash: [32]byte{2}, AllConnections: true}
	r.wantErr(s.CreateClient(ctx, broker.Client{
		ID:             "both",
		TokenHash:      [32]byte{3},
		AllConnections: true,
		Connections:    []broker.ID{"x"},
	}), broker.ErrInvalid)
	r.wantErr(s.CreateClient(ctx, all), nil)
	if got, err := s.GetClient(ctx, all.ID); err != nil || !got.AllConnections || len(got.Connections) != 0 {
		r.t.Fatalf("GetClient(all) = %+v, %v", got, err)
	}

	r.wantErr(s.DeleteClient(ctx, all.ID), nil)
}

// deletes checks deleting clients and forges.
func (r *contractRun) deletes() {
	ctx, s, f := r.ctx, r.s, r.forge

	r.wantErr(s.DeleteClient(ctx, r.client.ID), nil)
	r.wantErr(s.DeleteClient(ctx, r.client.ID), broker.ErrNotFound)
	r.wantErr(s.UpdateClient(ctx, r.client), broker.ErrNotFound)
	_, err := s.GetClient(ctx, r.client.ID)
	r.wantErr(err, broker.ErrNotFound)

	r.wantErr(s.DeleteForge(ctx, f.ID), nil)
	r.wantErr(s.DeleteForge(ctx, f.ID), broker.ErrNotFound)
	r.wantErr(s.UpdateForge(ctx, f), broker.ErrNotFound)
	_, err = s.GetForge(ctx, f.ID)
	r.wantErr(err, broker.ErrNotFound)
}

func cancellation(t *testing.T, s broker.Store) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	operations := []func() error{
		func() error { return s.CreateForge(ctx, broker.Forge{}) },
		func() error {
			_, err := s.GetForge(ctx, "")
			return err
		},
		func() error {
			_, err := s.ListForges(ctx)
			return err
		},
		func() error { return s.UpdateForge(ctx, broker.Forge{}) },
		func() error { return s.DeleteForge(ctx, "") },
		func() error { return s.CreateConnection(ctx, broker.Connection{}, nil) },
		func() error {
			_, err := s.GetConnection(ctx, "")
			return err
		},
		func() error {
			_, err := s.ListConnections(ctx)
			return err
		},
		func() error { return s.UpdateConnection(ctx, broker.Connection{}, nil) },
		func() error {
			_, err := s.GetCredential(ctx, "")
			return err
		},
		func() error { return s.DeleteConnection(ctx, "") },
		func() error { return s.CreateClient(ctx, broker.Client{}) },
		func() error {
			_, err := s.GetClient(ctx, "")
			return err
		},
		func() error {
			_, err := s.ListClients(ctx)
			return err
		},
		func() error { return s.UpdateClient(ctx, broker.Client{}) },
		func() error { return s.DeleteClient(ctx, "") },
	}
	for i, op := range operations {
		if err := op(); !errors.Is(err, context.Canceled) {
			t.Errorf("operation %d: %v", i, err)
		}
	}
}

func concurrent(t *testing.T, s broker.Store) {
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := broker.ID("f" + strconv.Itoa(i))
			f := broker.Forge{ID: id, Kind: forgeauth.GitHub, BaseURL: "https://example.com"}
			if err := s.CreateForge(ctx, f); err != nil {
				t.Error(err)
			}

			if _, err := s.ListForges(ctx); err != nil {
				t.Error(err)
			}
			if _, err := s.GetForge(ctx, id); err != nil {
				t.Error(err)
			}
			if err := s.DeleteForge(ctx, id); err != nil {
				t.Error(err)
			}
		}()
	}

	wg.Wait()
}

// Run checks persistence semantics, cancellation and concurrent access. New must
// return an empty store and register any required cleanup with t.Cleanup.
func Run(t *testing.T, newStore func(*testing.T) broker.Store) {
	t.Helper()
	t.Run("contract", func(t *testing.T) { contract(t, newStore(t)) })
	t.Run("cancellation", func(t *testing.T) { cancellation(t, newStore(t)) })
	t.Run("concurrent", func(t *testing.T) { concurrent(t, newStore(t)) })
}
