// Package store keeps agents, transcripts and raw turn output durably in
// one SQLite database.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	_ "modernc.org/sqlite" // registers the sqlite driver
)

// FileName is the database file in the data directory.
const FileName = "agents.db"

// ErrStorage reports a database that cannot be read or written. It never
// holds SQL, paths or content.
var ErrStorage = errors.New("agent storage failed")

// schemaVersion is the user_version of the current schema.
const schemaVersion = 1

// schema creates version 1. Agents and events are JSON documents; the
// columns beside them are what queries need.
const schema = `
CREATE TABLE agents (
	id TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL,
	data BLOB NOT NULL
) STRICT;
CREATE TABLE events (
	agent_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	data BLOB NOT NULL,
	PRIMARY KEY (agent_id, seq)
) STRICT, WITHOUT ROWID;
CREATE TABLE outputs (
	agent_id TEXT NOT NULL,
	turn INTEGER NOT NULL,
	data BLOB NOT NULL,
	PRIMARY KEY (agent_id, turn)
) STRICT;
PRAGMA user_version = 1;`

// options make writes durable when the process ends and overwrite removed
// content. One connection serializes writes, which are small.
const options = "_txlock=immediate&_pragma=journal_mode(wal)&_pragma=synchronous(normal)" +
	"&_pragma=secure_delete(1)&_pragma=busy_timeout(5000)"

// SQLite is an agent.Store in a SQLite database.
type SQLite struct {
	db *sql.DB
}

var _ agent.Store = (*SQLite)(nil)

// Open opens or creates the database in dir, private to the owner.
func Open(ctx context.Context, dir string) (*SQLite, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, ErrStorage
	}
	path := filepath.Join(abs, FileName)
	//nolint:gosec // the directory is the operator's and the name is fixed.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, ErrStorage
	}
	if f.Close() != nil || os.Chmod(path, 0o600) != nil {
		return nil, ErrStorage
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?"+options)
	if err != nil {
		return nil, ErrStorage
	}
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *SQLite) Close() error {
	if s.db.Close() != nil {
		return ErrStorage
	}
	return nil
}

func (s *SQLite) migrate(ctx context.Context) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		switch {
		case version == schemaVersion:
			return nil
		case version != 0:
			return fmt.Errorf("%w: schema version %d is not %d", ErrStorage, version, schemaVersion)
		}
		_, err := tx.ExecContext(ctx, schema)
		return err
	})
}

// tx runs fn in one transaction. Errors other than the agent package's
// become ErrStorage.
func (s *SQLite) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return clean(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return clean(ctx, err)
	}
	return clean(ctx, tx.Commit())
}

func clean(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, agent.ErrNotFound), errors.Is(err, agent.ErrTranscriptFull), errors.Is(err, ErrStorage):
		return err
	}
	return ErrStorage
}

// Put stores a.
func (s *SQLite) Put(ctx context.Context, a agent.Agent) error {
	data, err := json.Marshal(a)
	if err != nil {
		return ErrStorage
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agents (id, created_at, data) VALUES (?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET data = excluded.data`, a.ID, a.CreatedAt.UnixNano(), data)
		return err
	})
}

// Get returns an agent.
func (s *SQLite) Get(ctx context.Context, id string) (agent.Agent, error) {
	var a agent.Agent
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		a, err = get(ctx, tx, id)
		return err
	})
	return a, err
}

func get(ctx context.Context, tx *sql.Tx, id string) (agent.Agent, error) {
	var data []byte
	err := tx.QueryRowContext(ctx, "SELECT data FROM agents WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Agent{}, agent.ErrNotFound
	} else if err != nil {
		return agent.Agent{}, err
	}
	var a agent.Agent
	if json.Unmarshal(data, &a) != nil {
		return agent.Agent{}, ErrStorage
	}
	return a, nil
}

// List returns matching agents, oldest first. The manager bounds how many
// agents exist, so filtering happens here.
func (s *SQLite) List(ctx context.Context, f agent.Filter) ([]agent.Agent, error) {
	var list []agent.Agent
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT data FROM agents ORDER BY created_at, id")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var data []byte
			var a agent.Agent
			if err := rows.Scan(&data); err != nil {
				return err
			}
			if json.Unmarshal(data, &a) != nil {
				return ErrStorage
			}
			if f.Match(a) {
				list = append(list, a)
			}
		}
		return rows.Err()
	})
	return list, err
}

// Delete removes an agent, its events and its output.
func (s *SQLite) Delete(ctx context.Context, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, statement := range []string{
			"DELETE FROM events WHERE agent_id = ?",
			"DELETE FROM outputs WHERE agent_id = ?",
			"DELETE FROM agents WHERE id = ?",
		} {
			if _, err := tx.ExecContext(ctx, statement, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Append numbers and stores an event.
func (s *SQLite) Append(ctx context.Context, id string, e transcript.Event, limit int64) (transcript.Event, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := get(ctx, tx, id); err != nil {
			return err
		}
		var last int64
		err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq), 0) FROM events WHERE agent_id = ?", id).Scan(&last)
		if err != nil {
			return err
		}
		if last >= limit {
			return agent.ErrTranscriptFull
		}
		e.Seq = last + 1
		data, err := json.Marshal(e)
		if err != nil {
			return ErrStorage
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO events (agent_id, seq, data) VALUES (?, ?, ?)", id, e.Seq, data)
		return err
	})
	return e, err
}

// Events returns up to limit events after sequence number after.
func (s *SQLite) Events(ctx context.Context, id string, after int64, limit int) ([]transcript.Event, error) {
	var events []transcript.Event
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := get(ctx, tx, id); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx,
			"SELECT data FROM events WHERE agent_id = ? AND seq > ? ORDER BY seq LIMIT ?", id, after, limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var data []byte
			var e transcript.Event
			if err := rows.Scan(&data); err != nil {
				return err
			}
			if json.Unmarshal(data, &e) != nil {
				return ErrStorage
			}
			events = append(events, e)
		}
		return rows.Err()
	})
	return events, err
}

// PutOutput stores a turn's output and drops output older than keep turns.
func (s *SQLite) PutOutput(ctx context.Context, id string, turn int, o agent.Output, keep int) error {
	data, err := json.Marshal(o)
	if err != nil {
		return ErrStorage
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := get(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO outputs (agent_id, turn, data) VALUES (?, ?, ?)
			ON CONFLICT (agent_id, turn) DO UPDATE SET data = excluded.data`, id, turn, data)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM outputs WHERE agent_id = ? AND turn <= ?", id, turn-keep)
		return err
	})
}

// Output returns a turn's output.
func (s *SQLite) Output(ctx context.Context, id string, turn int) (agent.Output, error) {
	var o agent.Output
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var data []byte
		err := tx.QueryRowContext(ctx, "SELECT data FROM outputs WHERE agent_id = ? AND turn = ?", id, turn).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			return agent.ErrNotFound
		} else if err != nil {
			return err
		}
		if json.Unmarshal(data, &o) != nil {
			return ErrStorage
		}
		return nil
	})
	return o, err
}
