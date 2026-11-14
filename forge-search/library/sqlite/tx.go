package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"syscall"

	"github.com/r59q/forge-tools/forge-search/library/search"
	driver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// schemaVersion is the user_version of the current schema.
const schemaVersion = 1

// schema creates version 1. indexes holds one row per index with its
// counts. documents holds the identity, content and encoded terms of each
// document; its unique key gives the match order. terms holds one row per
// term of a document, so a search finds the documents of a term.
const schema = `
CREATE TABLE indexes (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	marker TEXT NOT NULL,
	documents INTEGER NOT NULL,
	bytes INTEGER NOT NULL
) STRICT;
CREATE TABLE documents (
	id INTEGER PRIMARY KEY,
	index_id INTEGER NOT NULL,
	kind TEXT NOT NULL,
	source TEXT NOT NULL,
	ref_id TEXT NOT NULL,
	version TEXT NOT NULL,
	content BLOB NOT NULL,
	terms BLOB NOT NULL,
	UNIQUE (index_id, kind, source, ref_id)
) STRICT;
CREATE TABLE terms (
	index_id INTEGER NOT NULL,
	term INTEGER NOT NULL,
	document_id INTEGER NOT NULL,
	PRIMARY KEY (index_id, term, document_id)
) STRICT, WITHOUT ROWID;
PRAGMA user_version = 1;`

// migrate brings the schema to schemaVersion. A database of a newer
// version returns ErrStorage.
func (s *Store) migrate(ctx context.Context) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}

		switch {
		case version == schemaVersion:
			return nil
		case version > schemaVersion:
			return ErrStorage
		}
		_, err := tx.ExecContext(ctx, schema)
		return err
	})
}

// write runs change in one transaction on the writer connection.
func (s *Store) write(ctx context.Context, change func(*sql.Tx) error) error {
	return transaction(ctx, s.writer, nil, change)
}

// read runs view in one read-only transaction, so that it sees one
// snapshot of the database.
func (s *Store) read(ctx context.Context, view func(*sql.Tx) error) error {
	return transaction(ctx, s.readers, &sql.TxOptions{ReadOnly: true}, view)
}

func transaction(ctx context.Context, db *sql.DB, options *sql.TxOptions, run func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, options)
	if err != nil {
		return clean(ctx, err)
	}
	// Rollback after Commit does nothing.
	defer func() { _ = tx.Rollback() }()

	if err := run(tx); err != nil {
		return clean(ctx, err)
	}
	return clean(ctx, tx.Commit())
}

// clean returns the category of err, so that no SQL, path or content
// leaves the package.
func clean(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	known := []error{
		context.Canceled, context.DeadlineExceeded,
		search.ErrNotFound, search.ErrExists, search.ErrFull, search.ErrInvalid,
		ErrStorage, ErrDiskFull, ErrReadOnly,
	}
	for _, category := range known {
		if errors.Is(err, category) {
			return category
		}
	}

	var failed *driver.Error
	if errors.As(err, &failed) {
		return codeError(failed.Code())
	}
	return ErrStorage
}

// codeError maps a SQLite result code to an error category.
func codeError(code int) error {
	switch code & 0xff {
	case sqlite3.SQLITE_FULL:
		return ErrDiskFull
	case sqlite3.SQLITE_READONLY, sqlite3.SQLITE_PERM, sqlite3.SQLITE_CANTOPEN:
		return ErrReadOnly
	}
	return ErrStorage
}

// fileError maps an error of the file system to an error category.
func fileError(err error) error {
	switch {
	case errors.Is(err, syscall.ENOSPC):
		return ErrDiskFull
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EROFS):
		return ErrReadOnly
	}
	return ErrStorage
}
