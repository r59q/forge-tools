// Package sqlite persists broker data using a pure-Go SQLite driver.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	_ "modernc.org/sqlite"
)

// Store is a concurrency-safe broker.Store. Open applies schema migrations.
// Credentials must already be sealed; the Store never receives plaintext.
// Use one service process per database and Close it after requests have stopped.
type Store struct{ db *sql.DB }

var _ broker.Store = (*Store)(nil)

// Open opens a local database file (not a SQLite URI) with owner-only permissions.
// The parent directory must exist. Context bounds initialization and migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, broker.ErrInvalid
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, broker.ErrStorage
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, broker.ErrStorage
	}
	if err = file.Close(); err != nil {
		return nil, broker.ErrStorage
	}
	if err = os.Chmod(abs, 0600); err != nil {
		return nil, broker.ErrStorage
	}
	u := url.URL{Scheme: "file", Path: abs}
	db, err := sql.Open("sqlite", u.String()+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, broker.ErrStorage
	}
	// One connection serializes transactions and keeps pragmas consistent.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, clean(err)
	}
	return s, nil
}

// Close releases the database connection.
func (s *Store) Close() error { return clean(s.db.Close()) }

// Format hides stored credential material and database internals.
func (*Store) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte("sqlite.Store")) }

func (s *Store) migrate(ctx context.Context) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if version > 1 {
			return broker.ErrStorage
		}
		if version == 1 {
			return nil
		}
		_, err := tx.ExecContext(ctx, `
   CREATE TABLE forges (id TEXT PRIMARY KEY NOT NULL, data BLOB NOT NULL);
   CREATE TABLE connections (id TEXT PRIMARY KEY NOT NULL, forge_id TEXT NOT NULL REFERENCES forges(id), data BLOB NOT NULL, sealed BLOB NOT NULL CHECK(length(sealed)>0));
   CREATE INDEX connections_forge ON connections(forge_id);
   CREATE TABLE clients (id TEXT PRIMARY KEY NOT NULL, data BLOB NOT NULL);
   PRAGMA user_version = 1;`)
		return err
	})
}

func clean(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, broker.ErrInvalid, broker.ErrConflict, broker.ErrNotFound} {
		if errors.Is(err, known) {
			return known
		}
	}
	return broker.ErrStorage
}
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return clean(err)
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return clean(err)
	}
	return clean(tx.Commit())
}

type reader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func get[T any](ctx context.Context, db reader, table string, id broker.ID) (T, error) {
	var result T
	var data []byte
	err := db.QueryRowContext(ctx, "SELECT data FROM "+table+" WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return result, broker.ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(data, &result)
	}
	return result, clean(err)
}
func list[T any](ctx context.Context, db reader, table string) ([]T, error) {
	rows, err := db.QueryContext(ctx, "SELECT data FROM "+table+" ORDER BY id")
	if err != nil {
		return nil, clean(err)
	}
	defer rows.Close()
	result := make([]T, 0)
	for rows.Next() {
		var data []byte
		var item T
		if err = rows.Scan(&data); err != nil {
			return nil, clean(err)
		}
		if err = json.Unmarshal(data, &item); err != nil {
			return nil, broker.ErrStorage
		}
		result = append(result, item)
	}
	return result, clean(rows.Err())
}
func encode(value any) ([]byte, error) { data, err := json.Marshal(value); return data, clean(err) }
func exists(ctx context.Context, tx *sql.Tx, table string, id broker.ID) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE id = ?", id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return broker.ErrNotFound
	}
	return nil
}
func unusedForge(ctx context.Context, tx *sql.Tx, id broker.ID) error {
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM connections WHERE forge_id = ?", id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return broker.ErrConflict
	}
	return nil
}

// CreateForge implements broker.Store.
func (s *Store) CreateForge(ctx context.Context, f broker.Forge) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if f.ID == "" || f.Kind == "" || f.BaseURL == "" {
			return broker.ErrInvalid
		}
		if err := exists(ctx, tx, "forges", f.ID); err == nil {
			return broker.ErrConflict
		} else if !errors.Is(err, broker.ErrNotFound) {
			return err
		}
		data, err := encode(f)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO forges(id,data) VALUES(?,?)", f.ID, data)
		return err
	})
}

// GetForge implements broker.Store.
func (s *Store) GetForge(ctx context.Context, id broker.ID) (broker.Forge, error) {
	return get[broker.Forge](ctx, s.db, "forges", id)
}

// ListForges implements broker.Store.
func (s *Store) ListForges(ctx context.Context) ([]broker.Forge, error) {
	return list[broker.Forge](ctx, s.db, "forges")
}

// UpdateForge implements broker.Store.
func (s *Store) UpdateForge(ctx context.Context, f broker.Forge) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		old, err := get[broker.Forge](ctx, tx, "forges", f.ID)
		if err != nil {
			return err
		}
		if f.Kind == "" || f.BaseURL == "" {
			return broker.ErrInvalid
		}
		if old.Kind != f.Kind || old.BaseURL != f.BaseURL || old.GitBaseURL != f.GitBaseURL {
			if err = unusedForge(ctx, tx, f.ID); err != nil {
				return err
			}
		}
		data, err := encode(f)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE forges SET data=? WHERE id=?", data, f.ID)
		return err
	})
}

// DeleteForge implements broker.Store.
func (s *Store) DeleteForge(ctx context.Context, id broker.ID) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := exists(ctx, tx, "forges", id); err != nil {
			return err
		}
		if err := unusedForge(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM forges WHERE id=?", id)
		return err
	})
}
func validStatus(status broker.Status) bool {
	return status == broker.StatusOK || status == broker.StatusInvalid || status == broker.StatusRevoked
}

// CreateConnection implements broker.Store.
func (s *Store) CreateConnection(ctx context.Context, c broker.Connection, sealed []byte) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if c.ID == "" || len(sealed) == 0 || !validStatus(c.Status) {
			return broker.ErrInvalid
		}
		if err := exists(ctx, tx, "connections", c.ID); err == nil {
			return broker.ErrConflict
		} else if !errors.Is(err, broker.ErrNotFound) {
			return err
		}
		if err := exists(ctx, tx, "forges", c.ForgeID); err != nil {
			return err
		}
		data, err := encode(c)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO connections(id,forge_id,data,sealed) VALUES(?,?,?,?)", c.ID, c.ForgeID, data, sealed)
		return err
	})
}

// GetConnection implements broker.Store.
func (s *Store) GetConnection(ctx context.Context, id broker.ID) (broker.Connection, error) {
	return get[broker.Connection](ctx, s.db, "connections", id)
}

// ListConnections implements broker.Store.
func (s *Store) ListConnections(ctx context.Context) ([]broker.Connection, error) {
	return list[broker.Connection](ctx, s.db, "connections")
}

// UpdateConnection implements broker.Store.
func (s *Store) UpdateConnection(ctx context.Context, c broker.Connection, sealed []byte) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		old, err := get[broker.Connection](ctx, tx, "connections", c.ID)
		if err != nil {
			return err
		}
		if old.ForgeID != c.ForgeID || !validStatus(c.Status) || (sealed != nil && len(sealed) == 0) {
			return broker.ErrInvalid
		}
		data, err := encode(c)
		if err != nil {
			return err
		}
		if sealed == nil {
			_, err = tx.ExecContext(ctx, "UPDATE connections SET data=? WHERE id=?", data, c.ID)
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE connections SET data=?,sealed=? WHERE id=?", data, sealed, c.ID)
		}
		return err
	})
}

// GetCredential implements broker.Store.
func (s *Store) GetCredential(ctx context.Context, id broker.ID) ([]byte, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, "SELECT sealed FROM connections WHERE id=?", id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, broker.ErrNotFound
	}
	return sealed, clean(err)
}

// DeleteConnection implements broker.Store.
func (s *Store) DeleteConnection(ctx context.Context, id broker.ID) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := exists(ctx, tx, "connections", id); err != nil {
			return err
		}
		clients, err := list[storedClient](ctx, tx, "clients")
		if err != nil {
			return err
		}
		for _, c := range clients {
			if !slices.Contains(c.Connections, id) {
				continue
			}
			c.Connections = slices.DeleteFunc(c.Connections, func(candidate broker.ID) bool { return candidate == id })
			data, err := encode(c)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE clients SET data=? WHERE id=?", data, c.ID); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM connections WHERE id=?", id)
		return err
	})
}

// Hash is persisted separately because Client deliberately omits TokenHash in JSON.
type storedClient struct {
	broker.Client
	Hash [32]byte
}

func (c storedClient) client() broker.Client { c.Client.TokenHash = c.Hash; return c.Client }
func checkClient(ctx context.Context, tx *sql.Tx, c broker.Client) error {
	if c.ID == "" || c.TokenHash == [32]byte{} || c.AllConnections && len(c.Connections) > 0 {
		return broker.ErrInvalid
	}
	for _, id := range c.Connections {
		if err := exists(ctx, tx, "connections", id); err != nil {
			return err
		}
	}
	return nil
}

// CreateClient implements broker.Store.
func (s *Store) CreateClient(ctx context.Context, c broker.Client) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := exists(ctx, tx, "clients", c.ID); err == nil {
			return broker.ErrConflict
		} else if !errors.Is(err, broker.ErrNotFound) {
			return err
		}
		if err := checkClient(ctx, tx, c); err != nil {
			return err
		}
		data, err := encode(storedClient{c, c.TokenHash})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO clients(id,data) VALUES(?,?)", c.ID, data)
		return err
	})
}

// GetClient implements broker.Store.
func (s *Store) GetClient(ctx context.Context, id broker.ID) (broker.Client, error) {
	c, err := get[storedClient](ctx, s.db, "clients", id)
	return c.client(), err
}

// ListClients implements broker.Store.
func (s *Store) ListClients(ctx context.Context) ([]broker.Client, error) {
	stored, err := list[storedClient](ctx, s.db, "clients")
	if err != nil {
		return nil, err
	}
	result := make([]broker.Client, 0, len(stored))
	for _, c := range stored {
		result = append(result, c.client())
	}
	return result, nil
}

// UpdateClient implements broker.Store.
func (s *Store) UpdateClient(ctx context.Context, c broker.Client) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := exists(ctx, tx, "clients", c.ID); err != nil {
			return err
		}
		if err := checkClient(ctx, tx, c); err != nil {
			return err
		}
		data, err := encode(storedClient{c, c.TokenHash})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE clients SET data=? WHERE id=?", data, c.ID)
		return err
	})
}

// DeleteClient implements broker.Store.
func (s *Store) DeleteClient(ctx context.Context, id broker.ID) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := exists(ctx, tx, "clients", id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM clients WHERE id=?", id)
		return err
	})
}
