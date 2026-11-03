package code

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// History returns one page of commits reachable from opts.Revision, in
// the forge's log order. The first page resolves the revision; Next
// continues from the same tip, so paging never drifts when the branch
// moves. Renames are not followed, except on GitLab before 18.10, which
// ignores follow=false and follows them for a single path unless its
// remove_file_commit_history_following flag is on. Path history on a
// Forgejo whose [git] COMMITS_RANGE_SIZE or [api] MAX_RESPONSE_ITEMS is
// not 50 is ErrUnsupported, as the forge would skip commits.
func (c *Client) History(ctx context.Context, r RepositoryRef, opts HistoryOptions) (HistoryPage, error) {
	if opts.PageSize < 0 || opts.PageSize > maxPageSize {
		return HistoryPage{}, fmt.Errorf("%w: page size %d, want 1 to %d", ErrInvalid, opts.PageSize, maxPageSize)
	}

	cur, pr, err := startCursor(opts)
	if err != nil {
		return HistoryPage{}, err
	}

	s, o, err := c.start(ctx, r)
	if err != nil {
		return HistoryPage{}, err
	}
	if opts.Cursor == "" {
		res, _, err := resolve(o, s.forge, pr)
		if err != nil {
			return HistoryPage{}, s.wrap(err)
		}

		cur.Tip = res.Commit
	}

	pos := historyPos{Page: cur.Page, Offset: cur.Offset}
	commits, next, more, err := s.forge.history(o, cur.Tip, cur.Path, cur.N, pos)
	if err != nil {
		return HistoryPage{}, s.wrap(err)
	}

	page := HistoryPage{Tip: cur.Tip, Commits: append([]Commit{}, commits...)}
	if more {
		if next == pos || !next.valid() {
			return HistoryPage{}, s.wrap(fmt.Errorf("code: history did not advance from page %d offset %d", pos.Page, pos.Offset))
		}

		cur.Page, cur.Offset = next.Page, next.Offset
		page.Next = cur.encode()
	}

	return page, nil
}

// startCursor returns the cursor History reads from: opts.Cursor, or a
// new one for opts.Revision, which it also returns parsed.
func startCursor(opts HistoryOptions) (historyCursor, revision, error) {
	if opts.Cursor != "" {
		cur, err := resumeCursor(opts)
		return cur, revision{}, err
	}

	path, err := cleanDir(opts.Path)
	if err != nil {
		return historyCursor{}, revision{}, err
	}

	pr, err := parseRevision(opts.Revision)
	if err != nil {
		return historyCursor{}, revision{}, err
	}

	cur := historyCursor{V: cursorVersion, Path: path, N: cmp(opts.PageSize, defaultPageSize)}

	return cur, pr, nil
}

// resumeCursor decodes opts.Cursor and checks that opts agree with it.
func resumeCursor(opts HistoryOptions) (historyCursor, error) {
	cur, err := decodeCursor(opts.Cursor)
	if err != nil {
		return historyCursor{}, err
	}
	if opts.PageSize != 0 && opts.PageSize != cur.N {
		return historyCursor{}, fmt.Errorf("%w: page size differs from the cursor's", ErrInvalid)
	}
	if opts.Path != "" && opts.Path != cur.Path {
		return historyCursor{}, fmt.Errorf("%w: path differs from the cursor's", ErrInvalid)
	}

	return cur, nil
}

// History cursor bounds.
const (
	cursorVersion   = 1
	maxCursorBytes  = 8192
	maxCursorPage   = 1000000
	maxCursorOffset = 10000
)

// historyCursor is the decoded form of HistoryPage.Next.
type historyCursor struct {
	V      int    `json:"v"`
	Tip    OID    `json:"tip"`
	Path   string `json:"path"`
	N      int    `json:"n"`
	Page   int    `json:"page"`
	Offset int    `json:"off"`
}

// encode returns the cursor as unpadded URL-safe base64 JSON.
func (c historyCursor) encode() string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

// decodeCursor decodes and checks a cursor. Only the exact encoding the
// library makes is accepted, so a tampered cursor is ErrInvalid.
func decodeCursor(s string) (historyCursor, error) {
	bad := fmt.Errorf("%w: history cursor", ErrInvalid)
	if len(s) > maxCursorBytes {
		return historyCursor{}, bad
	}

	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return historyCursor{}, bad
	}

	var c historyCursor
	if json.Unmarshal(data, &c) != nil || c.encode() != s {
		return historyCursor{}, bad
	}

	pos := historyPos{Page: c.Page, Offset: c.Offset}
	if c.V != cursorVersion || !c.Tip.Valid() || c.N < 1 || c.N > maxPageSize || !pos.valid() {
		return historyCursor{}, bad
	}
	if _, err := cleanDir(c.Path); err != nil {
		return historyCursor{}, bad
	}

	return c, nil
}

// valid reports whether p is within the cursor bounds.
func (p historyPos) valid() bool {
	return p.Page >= 0 && p.Page <= maxCursorPage && p.Offset >= 0 && p.Offset <= maxCursorOffset
}
