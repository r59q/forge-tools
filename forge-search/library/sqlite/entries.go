package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// storedIndex is the row of an index with its counts.
type storedIndex struct {
	id        int64
	documents int
	bytes     int64
}

// storedEntry is what a Put or Remove needs of a stored document. A zero id
// means there is none.
type storedEntry struct {
	id    int64
	bytes int64
	terms []search.Term
}

// Put implements search.Store.
func (s *Store) Put(ctx context.Context, index string, entry search.Entry, capacity search.Capacity) error {
	document := entry.Document
	if document.Content == nil {
		document.Content = []byte{}
	}
	encoded := encodeTerms(entry.Terms)

	return s.write(ctx, func(tx *sql.Tx) error {
		target, old, err := lookup(ctx, tx, index, document.Ref)
		if err != nil {
			return err
		}

		documents, size := growth(old, document)
		if target.documents+documents > capacity.Documents || target.bytes+size > capacity.Bytes {
			return search.ErrFull
		}

		id, err := saveDocument(ctx, tx, target.id, old.id, document, encoded)
		if err != nil {
			return err
		}

		added, removed := diffTerms(old.terms, entry.Terms)
		if err := changeTerms(ctx, tx, removeTermsSQL, target.id, id, removed); err != nil {
			return err
		}
		if err := changeTerms(ctx, tx, addTermsSQL, target.id, id, added); err != nil {
			return err
		}
		return changeCounts(ctx, tx, target.id, documents, size)
	})
}

// growth returns how many documents and bytes an index gains when document
// replaces old.
func growth(old storedEntry, document search.Document) (int, int64) {
	size := int64(len(document.Content))
	if old.id == 0 {
		return 1, size
	}
	return 0, size - old.bytes
}

// Remove implements search.Store.
func (s *Store) Remove(ctx context.Context, index string, ref search.Ref) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		target, old, err := lookup(ctx, tx, index, ref)
		if err != nil {
			return err
		}
		if old.id == 0 {
			return search.ErrNotFound
		}

		if err := changeTerms(ctx, tx, removeTermsSQL, target.id, old.id, old.terms); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM documents WHERE id = ?", old.id); err != nil {
			return err
		}
		return changeCounts(ctx, tx, target.id, -1, -old.bytes)
	})
}

// lookup returns an index and its entry with the identity of ref, which has
// a zero id when the index holds none.
func lookup(ctx context.Context, tx *sql.Tx, index string, ref search.Ref) (storedIndex, storedEntry, error) {
	var target storedIndex
	row := tx.QueryRowContext(ctx, "SELECT id, documents, bytes FROM indexes WHERE name = ?", index)
	if err := notFound(row.Scan(&target.id, &target.documents, &target.bytes)); err != nil {
		return storedIndex{}, storedEntry{}, err
	}

	var (
		old     storedEntry
		encoded []byte
	)
	row = tx.QueryRowContext(ctx,
		"SELECT id, length(content), terms FROM documents WHERE index_id = ? AND kind = ? AND source = ? AND ref_id = ?",
		target.id, string(ref.Kind), ref.Source, ref.ID)

	err := row.Scan(&old.id, &old.bytes, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return target, storedEntry{}, nil
	}
	if err != nil {
		return storedIndex{}, storedEntry{}, err
	}

	old.terms, err = decodeTerms(encoded)
	return target, old, err
}

// saveDocument stores a document in place of the one with row ID old, or
// as a new row when old is zero, and returns its row ID.
func saveDocument(ctx context.Context, tx *sql.Tx, index, old int64, document search.Document, terms []byte) (int64, error) {
	if old != 0 {
		_, err := tx.ExecContext(ctx, "UPDATE documents SET version = ?, content = ?, terms = ? WHERE id = ?",
			document.Version, document.Content, terms, old)
		return old, err
	}

	result, err := tx.ExecContext(ctx,
		"INSERT INTO documents(index_id, kind, source, ref_id, version, content, terms) VALUES(?, ?, ?, ?, ?, ?, ?)",
		index, string(document.Kind), document.Source, document.ID, document.Version, document.Content, terms)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// Statements that add or remove the terms of a document, given as a JSON
// array, so that a document needs one statement whatever its size.
const (
	addTermsSQL    = "INSERT INTO terms(index_id, term, document_id) SELECT ?1, value, ?2 FROM json_each(?3)"
	removeTermsSQL = "DELETE FROM terms WHERE index_id = ?1 AND document_id = ?2 AND term IN (SELECT value FROM json_each(?3))"
)

func changeTerms(ctx context.Context, tx *sql.Tx, statement string, index, document int64, terms []search.Term) error {
	if len(terms) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, statement, index, document, jsonTerms(terms))
	return err
}

func changeCounts(ctx context.Context, tx *sql.Tx, index int64, documents int, bytes int64) error {
	_, err := tx.ExecContext(ctx, "UPDATE indexes SET documents = documents + ?, bytes = bytes + ? WHERE id = ?",
		documents, bytes, index)
	return err
}
