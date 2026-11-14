package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// queryer runs queries in a transaction or on a database.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// CreateIndex implements search.Store.
func (s *Store) CreateIndex(ctx context.Context, name string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := indexID(ctx, tx, name)
		switch {
		case err == nil:
			return search.ErrExists
		case !errors.Is(err, search.ErrNotFound):
			return err
		}

		_, err = tx.ExecContext(ctx, "INSERT INTO indexes(name, marker, documents, bytes) VALUES(?, '', 0, 0)", name)
		return err
	})
}

// DeleteIndex implements search.Store.
func (s *Store) DeleteIndex(ctx context.Context, name string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		id, err := indexID(ctx, tx, name)
		if err != nil {
			return err
		}

		for {
			batch, err := someDocuments(ctx, tx, id)
			if err != nil || len(batch) == 0 {
				if err == nil {
					_, err = tx.ExecContext(ctx, "DELETE FROM indexes WHERE id = ?", id)
				}
				return err
			}
			for _, old := range batch {
				if err := drop(ctx, tx, id, old); err != nil {
					return err
				}
			}
		}
	})
}

// someDocuments returns up to deleteBatch documents of an index, so that
// deleting an index holds no more than those in memory.
func someDocuments(ctx context.Context, tx *sql.Tx, index int64) ([]storedEntry, error) {
	const deleteBatch = 64

	rows, err := tx.QueryContext(ctx, "SELECT id, terms FROM documents WHERE index_id = ? LIMIT ?", index, deleteBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var batch []storedEntry
	for rows.Next() {
		var old storedEntry
		if err := rows.Scan(&old.id, &old.terms); err != nil {
			return nil, err
		}
		batch = append(batch, old)
	}
	return batch, rows.Err()
}

// Indexes implements search.Store.
func (s *Store) Indexes(ctx context.Context) ([]search.IndexInfo, error) {
	infos := []search.IndexInfo{}

	err := s.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT name, marker, documents, bytes FROM indexes ORDER BY name")
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var info search.IndexInfo
			if err := rows.Scan(&info.Name, &info.Marker, &info.Documents, &info.Bytes); err != nil {
				return err
			}
			infos = append(infos, info)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return infos, nil
}

// Index implements search.Store.
func (s *Store) Index(ctx context.Context, name string) (search.IndexInfo, error) {
	info := search.IndexInfo{Name: name}

	err := s.read(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, "SELECT marker, documents, bytes FROM indexes WHERE name = ?", name)
		return notFound(row.Scan(&info.Marker, &info.Documents, &info.Bytes))
	})
	if err != nil {
		return search.IndexInfo{}, err
	}
	return info, nil
}

// SetMarker implements search.Store.
func (s *Store) SetMarker(ctx context.Context, name, marker string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE indexes SET marker = ? WHERE name = ?", marker, name)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err == nil && changed == 0 {
			return search.ErrNotFound
		}
		return err
	})
}

// indexID returns the row ID of the index with this name.
func indexID(ctx context.Context, db queryer, name string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, "SELECT id FROM indexes WHERE name = ?", name).Scan(&id)
	return id, notFound(err)
}

// notFound turns a missing row into search.ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return search.ErrNotFound
	}
	return err
}
