package broker_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	var s broker.MemoryStore
	f := broker.Forge{ID: "f", Kind: forgeauth.GitHub, BaseURL: "https://example.com"}
	c := broker.Connection{ID: "c", ForgeID: f.ID, Status: broker.StatusOK}
	client := broker.Client{ID: "client", TokenHash: [32]byte{1}, Connections: []broker.ID{c.ID}}
	wantErr := func(err, want error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	}
	wantErr(s.CreateForge(ctx, broker.Forge{}), broker.ErrInvalid)
	wantErr(s.CreateConnection(ctx, c, []byte("ciphertext")), broker.ErrNotFound)
	wantErr(s.CreateClient(ctx, client), broker.ErrNotFound)
	wantErr(s.CreateForge(ctx, f), nil)
	wantErr(s.CreateForge(ctx, f), broker.ErrConflict)
	f.Name = "renamed"
	wantErr(s.UpdateForge(ctx, f), nil)
	if got, err := s.GetForge(ctx, f.ID); err != nil || got != f {
		t.Fatalf("GetForge = %v, %v", got, err)
	}
	if got, err := s.ListForges(ctx); err != nil || len(got) != 1 || got[0] != f {
		t.Fatalf("ListForges = %v, %v", got, err)
	}

	sealed := []byte("ciphertext")
	wantErr(s.CreateConnection(ctx, c, sealed), nil)
	sealed[0] = 'X'
	wantErr(s.CreateConnection(ctx, c, sealed), broker.ErrConflict)
	wantErr(s.DeleteForge(ctx, f.ID), broker.ErrConflict)
	changed := f
	changed.BaseURL = "https://another.example.com"
	wantErr(s.UpdateForge(ctx, changed), broker.ErrConflict)
	changed = f
	changed.Kind = forgeauth.GitLab
	wantErr(s.UpdateForge(ctx, changed), broker.ErrConflict)
	f.Name = "safe rename"
	wantErr(s.UpdateForge(ctx, f), nil)
	gotSealed, err := s.GetCredential(ctx, c.ID)
	if err != nil || string(gotSealed) != "ciphertext" {
		t.Fatal("CreateConnection retained caller's ciphertext slice")
	}
	gotSealed[0] = 'X'
	c.Name = "renamed"
	wantErr(s.UpdateConnection(ctx, c, nil), nil)
	gotSealed, err = s.GetCredential(ctx, c.ID)
	if err != nil || string(gotSealed) != "ciphertext" {
		t.Fatal("GetCredential exposed stored slice or nil update lost ciphertext")
	}
	got, err := s.GetConnection(ctx, c.ID)
	if err != nil || got != c {
		t.Fatalf("GetConnection = %v, %v", got, err)
	}
	if got, err := s.ListConnections(ctx); err != nil || len(got) != 1 || got[0] != c {
		t.Fatalf("ListConnections = %v, %v", got, err)
	}
	changedConn := c
	changedConn.ForgeID = "other"
	wantErr(s.UpdateConnection(ctx, changedConn, nil), broker.ErrInvalid)
	wantErr(s.UpdateConnection(ctx, c, []byte{}), broker.ErrInvalid)
	replacement := []byte("replacement")
	wantErr(s.UpdateConnection(ctx, c, replacement), nil)
	replacement[0] = 'X'
	gotSealed, err = s.GetCredential(ctx, c.ID)
	if err != nil || !bytes.Equal(gotSealed, []byte("replacement")) {
		t.Fatal("UpdateConnection retained caller's slice")
	}

	wantErr(s.CreateClient(ctx, client), nil)
	wantErr(s.CreateClient(ctx, client), broker.ErrConflict)
	client.Connections[0] = "missing"
	storedClient, err := s.GetClient(ctx, client.ID)
	if err != nil || !slices.Equal(storedClient.Connections, []broker.ID{c.ID}) {
		t.Fatal("CreateClient retained caller's scope slice")
	}
	storedClient.Name = "updated"
	wantErr(s.UpdateClient(ctx, storedClient), nil)
	storedClient.Connections[0] = "missing"
	clients, err := s.ListClients(ctx)
	if err != nil || len(clients) != 1 || clients[0].Name != "updated" || clients[0].Connections[0] != c.ID {
		t.Fatal("UpdateClient retained caller's scope slice")
	}
	clients[0].Connections[0] = "missing"
	storedClient, err = s.GetClient(ctx, client.ID)
	if err != nil || storedClient.Connections[0] != c.ID {
		t.Fatal("ListClients exposed stored scope")
	}
	storedClient.Connections[0] = "missing"
	wantErr(s.UpdateClient(ctx, storedClient), broker.ErrNotFound)
	wantErr(s.DeleteConnection(ctx, c.ID), nil)
	storedClient, err = s.GetClient(ctx, client.ID)
	if err != nil || len(storedClient.Connections) != 0 {
		t.Fatal("DeleteConnection did not remove client scope")
	}
	_, err = s.GetCredential(ctx, c.ID)
	wantErr(err, broker.ErrNotFound)
	_, err = s.GetConnection(ctx, c.ID)
	wantErr(err, broker.ErrNotFound)
	wantErr(s.DeleteConnection(ctx, c.ID), broker.ErrNotFound)
	wantErr(s.UpdateConnection(ctx, c, nil), broker.ErrNotFound)
	wantErr(s.DeleteClient(ctx, client.ID), nil)
	wantErr(s.DeleteClient(ctx, client.ID), broker.ErrNotFound)
	wantErr(s.UpdateClient(ctx, client), broker.ErrNotFound)
	_, err = s.GetClient(ctx, client.ID)
	wantErr(err, broker.ErrNotFound)
	wantErr(s.DeleteForge(ctx, f.ID), nil)
	wantErr(s.DeleteForge(ctx, f.ID), broker.ErrNotFound)
	wantErr(s.UpdateForge(ctx, f), broker.ErrNotFound)
	_, err = s.GetForge(ctx, f.ID)
	wantErr(err, broker.ErrNotFound)
}

func TestMemoryStoreCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var s broker.MemoryStore
	operations := []func() error{
		func() error { return s.CreateForge(ctx, broker.Forge{}) },
		func() error { _, err := s.GetForge(ctx, ""); return err },
		func() error { _, err := s.ListForges(ctx); return err },
		func() error { return s.UpdateForge(ctx, broker.Forge{}) },
		func() error { return s.DeleteForge(ctx, "") },
		func() error { return s.CreateConnection(ctx, broker.Connection{}, nil) },
		func() error { _, err := s.GetConnection(ctx, ""); return err },
		func() error { _, err := s.ListConnections(ctx); return err },
		func() error { return s.UpdateConnection(ctx, broker.Connection{}, nil) },
		func() error { _, err := s.GetCredential(ctx, ""); return err },
		func() error { return s.DeleteConnection(ctx, "") },
		func() error { return s.CreateClient(ctx, broker.Client{}) },
		func() error { _, err := s.GetClient(ctx, ""); return err },
		func() error { _, err := s.ListClients(ctx); return err },
		func() error { return s.UpdateClient(ctx, broker.Client{}) },
		func() error { return s.DeleteClient(ctx, "") },
	}
	for i, op := range operations {
		if err := op(); !errors.Is(err, context.Canceled) {
			t.Errorf("operation %d: %v", i, err)
		}
	}
}

func TestMemoryStoreConcurrent(t *testing.T) {
	ctx := context.Background()
	var s broker.MemoryStore
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := broker.ID(string(rune('a' + i)))
			f := broker.Forge{ID: id, Kind: forgeauth.GitHub, BaseURL: "https://example.com"}
			if err := s.CreateForge(ctx, f); err != nil {
				t.Error(err)
			}
			s.ListForges(ctx)
			s.GetForge(ctx, id)
			if err := s.DeleteForge(ctx, id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
