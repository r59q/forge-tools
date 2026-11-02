package sqlite_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/broker/sqlite"
	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/broker/storetest"
)

func TestStore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) broker.Store {
		s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "broker.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		})
		return s
	})
}

func TestRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "broker.db")
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	f := broker.Forge{ID: "f", Kind: "github", BaseURL: "https://example.com/api/v3", GitBaseURL: "https://example.com"}
	c := broker.Connection{ID: "c", ForgeID: "f", Status: broker.StatusOK}
	client := broker.Client{ID: "client", TokenHash: [32]byte{1}, Connections: []broker.ID{"c"}}
	for _, err := range []error{s.CreateForge(ctx, f), s.CreateConnection(ctx, c, []byte("ciphertext")), s.CreateClient(ctx, client), s.Close()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err = sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.GetForge(ctx, f.ID); err != nil || got != f {
		t.Fatalf("forge: %v %v", got, err)
	}
	if got, err := s.GetConnection(ctx, c.ID); err != nil || got != c {
		t.Fatalf("connection: %v %v", got, err)
	}
	if got, err := s.GetClient(ctx, client.ID); err != nil || !reflect.DeepEqual(got, client) {
		t.Fatalf("client: %v %v", got, err)
	}
	if got, err := s.GetCredential(ctx, c.ID); err != nil || string(got) != "ciphertext" {
		t.Fatal("lost sealed credential")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("database must have owner-only permissions")
	}
}

func TestLegacyForgeWithoutGitBaseURL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "broker.db")
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A record written before GitBaseURL existed.
	_, err = db.ExecContext(ctx, "INSERT INTO forges(id,data) VALUES(?,?)", "old", `{"ID":"old","Kind":"github","BaseURL":"https://api.github.com","Name":"n"}`)
	if cerr := db.Close(); err != nil || cerr != nil {
		t.Fatal(err, cerr)
	}
	s, err = sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := broker.Forge{ID: "old", Kind: "github", BaseURL: "https://api.github.com", Name: "n"}
	if got, err := s.GetForge(ctx, "old"); err != nil || got != want {
		t.Fatalf("legacy forge = %+v, %v", got, err)
	}
}
