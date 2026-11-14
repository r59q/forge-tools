package storetest

import (
	"bytes"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// sentinel is content that must leave the store with its entry.
const sentinel = "sentinel-7f3a9c"

// removedContent checks that a replaced entry, a removed entry and a deleted
// index leave no content and no terms behind.
func (r *run) removedContent() {
	ctx, s := r.ctx, r.store
	r.create("a", "b", "c")

	for _, index := range []string{"a", "b", "c"} {
		r.put(index,
			entry("x", "v1", "before "+sentinel+" after", 11, 12),
			entry("y", "v1", sentinel, 11, 12),
		)
	}
	if got := r.traces(); got != 6 {
		r.t.Fatalf("%d documents hold the sentinel, want 6", got)
	}

	r.put("a", entry("x", "v2", "clean", 13))
	r.wantErr(s.Remove(ctx, "a", entry("y", "", "").Document.Ref), nil)
	r.wantErr(s.DeleteIndex(ctx, "b"), nil)
	r.wantErr(s.DeleteIndex(ctx, "c"), nil)
	r.create("c")

	if got := r.traces(); got != 0 {
		r.t.Fatalf("%d documents still hold removed content", got)
	}
	for _, index := range []string{"a", "c"} {
		r.wantIDs(index, terms(11))
		r.wantIDs(index, terms(12))
	}
	r.wantIDs("a", search.Selection{}, "x")
	r.wantInfo("a", search.IndexInfo{Name: "a", Documents: 1, Bytes: 5})
	r.wantInfo("c", search.IndexInfo{Name: "c"})
}

// traces counts the documents, over every index, whose content holds the
// sentinel.
func (r *run) traces() int {
	r.t.Helper()

	indexes, err := r.store.Indexes(r.ctx)
	r.wantErr(err, nil)

	count := 0
	for _, index := range indexes {
		for _, document := range r.documents(index.Name, search.Selection{}) {
			if bytes.Contains(document.Content, []byte(sentinel)) {
				count++
			}
		}
	}
	return count
}
