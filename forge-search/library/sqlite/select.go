package sqlite

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// Refs implements search.Store.
func (s *Store) Refs(ctx context.Context, index string, selection search.Selection, visit func(search.Ref) bool) error {
	return s.read(ctx, func(tx *sql.Tx) error {
		return selected(ctx, tx, index, selection, func(_ int64, ref search.Ref) (bool, error) {
			return visit(ref), nil
		})
	})
}

// contentBatch is how many documents Documents reads the content of in one
// statement. A statement reuses its buffers from one document to the next.
const contentBatch = 64

// listed is a selected document without its content.
type listed struct {
	id  int64
	ref search.Ref
}

// Documents implements search.Store. It lists the selected documents first
// and then reads their content in batches, one document at a time, so that
// neither sorting nor a batch holds content in memory.
func (s *Store) Documents(ctx context.Context, index string, selection search.Selection, visit func(search.Document) bool) error {
	return s.read(ctx, func(tx *sql.Tx) error {
		batch := make([]listed, 0, contentBatch)
		more := true

		err := selected(ctx, tx, index, selection, func(id int64, ref search.Ref) (bool, error) {
			batch = append(batch, listed{id: id, ref: ref})
			if len(batch) < contentBatch {
				return true, nil
			}
			var err error
			more, err = withContent(ctx, tx, batch, visit)
			batch = batch[:0]
			return more, err
		})
		if err != nil || !more || len(batch) == 0 {
			return err
		}
		_, err = withContent(ctx, tx, batch, visit)
		return err
	})
}

// contentSQL reads the content of documents, given as a JSON array of row
// IDs, in array order: the cross join keeps the array first.
const contentSQL = "SELECT j.key, d.content FROM json_each(?) AS j CROSS JOIN documents d ON d.id = j.value"

// withContent calls visit with each document of batch and its content, in
// order, and reports whether visit asked for more.
func withContent(ctx context.Context, tx *sql.Tx, batch []listed, visit func(search.Document) bool) (bool, error) {
	ids := []byte{'['}
	for position, document := range batch {
		if position > 0 {
			ids = append(ids, ',')
		}
		ids = strconv.AppendInt(ids, document.id, 10)
	}

	rows, err := tx.QueryContext(ctx, contentSQL, string(append(ids, ']')))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	position := 0
	for ; rows.Next(); position++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}

		var (
			key     int
			content []byte
		)
		if err := rows.Scan(&key, &content); err != nil {
			return false, err
		}
		if key != position || position >= len(batch) {
			return false, ErrStorage
		}
		if !visit(search.Document{Ref: batch[position].ref, Content: content}) {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if position != len(batch) {
		return false, ErrStorage
	}
	return true, nil
}

// selected calls visit with the row ID and identity of each selected
// document, in match order, until visit returns false or an error.
func selected(
	ctx context.Context, tx *sql.Tx, index string, selection search.Selection, visit func(int64, search.Ref) (bool, error),
) error {
	id, err := indexID(ctx, tx, index)
	if err != nil {
		return err
	}
	query, args := selectionQuery(id, selection)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}

		var (
			row int64
			ref search.Ref
		)
		if err := rows.Scan(&row, &ref.Kind, &ref.Source, &ref.ID, &ref.Version); err != nil {
			return err
		}
		if more, err := visit(row, ref); err != nil || !more {
			return err
		}
	}
	return rows.Err()
}

// selectionQuery returns the query and arguments that list the selected
// documents of an index in match order. With terms, the full-text index
// finds the documents that hold all of them; the cross join keeps it first.
func selectionQuery(index int64, selection search.Selection) (string, []any) {
	where := conditions{}
	from := "documents d"

	if len(selection.Terms) > 0 {
		from = "postings CROSS JOIN documents d ON d.id = postings.rowid"
		where.add("postings MATCH ?", tokens(index, selection.Terms))
	}

	where.filter(index, selection.Filter)
	query := "SELECT d.id, d.kind, d.source, d.ref_id, d.version FROM " + from +
		" WHERE " + strings.Join(where.clauses, " AND ") +
		" ORDER BY d.kind, d.source, d.ref_id"
	return query, where.args
}

// conditions collects the clauses of a WHERE and their arguments.
type conditions struct {
	clauses []string
	args    []any
}

func (c *conditions) add(clause string, args ...any) {
	c.clauses = append(c.clauses, clause)
	c.args = append(c.args, args...)
}

// filter adds the clauses of a filter. Text compares as bytes, and a prefix
// compares as a blob so that its length counts bytes.
func (c *conditions) filter(index int64, filter search.Filter) {
	c.add("d.index_id = ?", index)

	if filter.Kind != "" {
		c.add("d.kind = ?", string(filter.Kind))
	}
	if filter.Source != "" {
		c.add("d.source = ?", filter.Source)
	}
	if filter.ID != "" {
		c.add("d.ref_id = ?", filter.ID)
	}
	if filter.IDPrefix != "" {
		prefix := []byte(filter.IDPrefix)
		c.add("substr(CAST(d.ref_id AS BLOB), 1, length(?)) = ?", prefix, prefix)
	}
}
