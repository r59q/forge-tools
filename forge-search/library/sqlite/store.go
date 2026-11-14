// Package sqlite keeps forge-search indexes in an embedded SQLite database,
// using a pure-Go driver. All indexes share one database file in a directory
// the caller supplies.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// FileName is the name of the database file in the data directory. SQLite
// keeps its write-ahead log and shared memory beside it, in FileName with
// "-wal" and "-shm" appended.
const FileName = "search.db"

// Storage error categories. Match them with errors.Is. Their messages never
// contain SQL, file paths or document content.
var (
	// ErrStorage reports a database that cannot be opened, read or written,
	// or that was created by a newer version of this package.
	ErrStorage = errors.New("search storage failed")

	// ErrDiskFull reports a disk without space for a change.
	ErrDiskFull = errors.New("search storage is out of space")

	// ErrReadOnly reports a database or directory that cannot be written.
	ErrReadOnly = errors.New("search storage is read-only")
)

// Store is a search.Store in a SQLite database. It is safe for concurrent
// use: searches read a snapshot each and do not wait for updates, which run
// one at a time. Use one process per data directory and Close the store when
// requests have stopped.
type Store struct {
	writer  *sql.DB
	readers *sql.DB
}

var _ search.Store = (*Store)(nil)

// Open opens the store in dir, an existing directory, creating its database
// file when there is none. The file and the files SQLite keeps beside it are
// readable and writable only by the owner. The context bounds opening and
// schema migrations.
func Open(ctx context.Context, dir string) (*Store, error) {
	path, err := createFile(dir)
	if err != nil {
		return nil, err
	}

	writer, err := sql.Open("sqlite", dsn(path, writerOptions))
	if err != nil {
		return nil, ErrStorage
	}
	// One writer connection serializes updates.
	writer.SetMaxOpenConns(1)

	s := &Store{writer: writer}
	if err := s.setUp(ctx); err != nil {
		_ = writer.Close()
		return nil, clean(ctx, err)
	}

	s.readers, err = sql.Open("sqlite", dsn(path, readerOptions))
	if err != nil {
		_ = writer.Close()
		return nil, ErrStorage
	}
	s.readers.SetMaxOpenConns(max(4, runtime.GOMAXPROCS(0)))
	return s, nil
}

// Close releases the database. SQLite then copies the write-ahead log into
// the database file and removes the log.
func (s *Store) Close() error {
	return clean(context.Background(), errors.Join(s.readers.Close(), s.writer.Close()))
}

// Checkpoint removes what is left of removed documents and empties the
// write-ahead log. Their content is overwritten in the database file at
// once, but the log may still hold it, and the full-text index their terms,
// until a checkpoint; after Checkpoint returns, neither file holds them.
//
// It rewrites the full-text index, which takes time in proportion to the
// size of all indexes, and updates wait for it. It then waits for searches
// that read an older snapshot, and returns ErrStorage when they do not
// finish in time.
func (s *Store) Checkpoint(ctx context.Context) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO postings(postings) VALUES('optimize')")
		return err
	})
	if err != nil {
		return err
	}

	var busy, frames, copied int
	err = s.writer.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &copied)
	if err == nil && busy != 0 {
		err = ErrStorage
	}
	return clean(ctx, err)
}

// setUp gives a new database its page size and write-ahead log, which the
// file keeps, and migrates the schema. The page size has to come first, so
// it cannot be a connection option: the driver runs those by name.
func (s *Store) setUp(ctx context.Context) error {
	for _, statement := range []string{"PRAGMA page_size = 65536", "PRAGMA journal_mode = WAL"} {
		if _, err := s.writer.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return s.migrate(ctx)
}

// Connection options. secure_delete overwrites removed content, temp_store
// keeps temporary data, which may hold content, out of files, and
// synchronous(normal) is durable when the process ends, not on power loss.
// Readers map the file into memory, so that reading a page takes no system
// call.
const (
	writerOptions = "_txlock=immediate&_pragma=synchronous(normal)" +
		"&_pragma=secure_delete(1)&_pragma=temp_store(memory)&_pragma=busy_timeout(5000)"
	readerOptions = "_pragma=mmap_size(1073741824)&_pragma=query_only(1)&_pragma=secure_delete(1)" +
		"&_pragma=temp_store(memory)&_pragma=busy_timeout(5000)"
)

func dsn(path, options string) string {
	location := url.URL{Scheme: "file", Path: path}
	return location.String() + "?" + options
}

// createFile creates the database file in dir when it is missing, and
// makes it private to the owner. It returns the path of the file.
func createFile(dir string) (string, error) {
	if dir == "" {
		return "", search.ErrInvalid
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", ErrStorage
	}
	path := filepath.Join(abs, FileName)

	//nolint:gosec // the directory is the caller's and the file name is fixed.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fileError(err)
	}
	if err := file.Close(); err != nil {
		return "", fileError(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fileError(err)
	}
	return path, nil
}
