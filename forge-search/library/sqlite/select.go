package sqlite

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// probeRows bounds how many documents of a term a search counts to find
// the rarest term of a query.
const probeRows = 128

// Refs implements search.Store.
func (s *Store) Refs(ctx context.Context, index string, selection search.Selection, visit func(search.Ref) bool) error {
	return s.read(ctx, func(tx *sql.Tx) error {
		return selected(ctx, tx, index, selection, func(_ int64, ref search.Ref) (bool, error) {
			return visit(ref), nil
		})
	})
}

// Documents implements search.Store. It lists the selected documents first
// and then reads each content on its own, so that sorting never holds
// content in memory.
func (s *Store) Documents(ctx context.Context, index string, selection search.Selection, visit func(search.Document) bool) error {
	return s.read(ctx, func(tx *sql.Tx) error {
		content, err := tx.PrepareContext(ctx, "SELECT content FROM documents WHERE id = ?")
		if err != nil {
			return err
		}
		defer content.Close()

		return selected(ctx, tx, index, selection, func(id int64, ref search.Ref) (bool, error) {
			document := search.Document{Ref: ref}
			if err := content.QueryRowContext(ctx, id).Scan(&document.Content); err != nil {
				return false, err
			}
			return visit(document), nil
		})
	})
}

// selected calls visit with the row ID and identity of each selected
// document, in match order, until visit returns false or an error.
func selected(ctx context.Context, tx *sql.Tx, index string, selection search.Selection, visit func(int64, search.Ref) (bool, error)) error {
	id, err := indexID(ctx, tx, index)
	if err != nil {
		return err
	}
	query, args, err := selectionQuery(ctx, tx, id, selection)
	if err != nil || query == "" {
		return err
	}

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

// holdsRestSQL selects a candidate document t that holds every term of a
// JSON array. Terms are checked in array order and the first missing one
// ends the check.
const holdsRestSQL = `NOT EXISTS (
	SELECT 1 FROM json_each(?) AS j WHERE NOT EXISTS (
		SELECT 1 FROM terms u WHERE u.index_id = ? AND u.term = j.value AND u.document_id = t.document_id))`

// selectionQuery returns the query and arguments that list the selected
// documents of an index in match order. A selection that cannot match gives
// an empty query.
//
// With terms, the documents of the rarest term are the candidates, and each
// is checked for the other terms, rarest first.
func selectionQuery(ctx context.Context, tx *sql.Tx, index int64, selection search.Selection) (string, []any, error) {
	where := conditions{}
	from := "documents d"

	if len(selection.Terms) > 0 {
		rarest, err := byRarity(ctx, tx, index, selection.Terms)
		if err != nil || rarest == nil {
			return "", nil, err
		}

		from = "terms t JOIN documents d ON d.id = t.document_id"
		where.add("t.index_id = ? AND t.term = ?", index, rarest[0])
		if len(rarest) > 1 {
			where.add(holdsRestSQL, jsonTerms(rarest[1:]), index)
		}
	}

	where.filter(index, selection.Filter)
	query := "SELECT d.id, d.kind, d.source, d.ref_id, d.version FROM " + from +
		" WHERE " + strings.Join(where.clauses, " AND ") +
		" ORDER BY d.kind, d.source, d.ref_id"
	return query, where.args, nil
}

// byRarity returns the terms ordered by how many documents of the index
// hold them, counting up to probeRows, the rarest first. It returns nil when
// a term is in no document.
func byRarity(ctx context.Context, tx *sql.Tx, index int64, terms []search.Term) ([]search.Term, error) {
	probe, err := tx.PrepareContext(ctx,
		"SELECT count(*) FROM (SELECT 1 FROM terms WHERE index_id = ? AND term = ? LIMIT ?)")
	if err != nil {
		return nil, err
	}
	defer probe.Close()

	counts := make(map[search.Term]int, len(terms))
	for _, term := range terms {
		var count int
		if err := probe.QueryRowContext(ctx, index, term, probeRows).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, nil
		}
		counts[term] = count
	}

	rarest := slices.Clone(terms)
	slices.SortStableFunc(rarest, func(a, b search.Term) int { return counts[a] - counts[b] })
	return rarest, nil
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
