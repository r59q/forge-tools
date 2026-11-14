package search

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
)

// MemoryStore is a Store that keeps its indexes in memory. The zero value is
// ready to use.
type MemoryStore struct {
	mu      sync.RWMutex
	indexes map[string]*memoryIndex
}

// memoryIndex holds the entries of one index. postings lists, for each term,
// the entries that hold it.
type memoryIndex struct {
	marker   string
	bytes    int64
	entries  map[memoryKey]*memoryEntry
	postings map[Term][]*memoryEntry
}

type memoryKey struct {
	kind   Kind
	source string
	id     string
}

// memoryEntry is never changed after it is stored, so readers may use it
// without the lock.
type memoryEntry struct {
	document Document
	terms    []Term
}

func keyOf(ref Ref) memoryKey {
	return memoryKey{kind: ref.Kind, source: ref.Source, id: ref.ID}
}

// CreateIndex implements Store.
func (s *MemoryStore) CreateIndex(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.indexes[name]; exists {
		return ErrExists
	}
	if s.indexes == nil {
		s.indexes = map[string]*memoryIndex{}
	}
	s.indexes[name] = &memoryIndex{
		entries:  map[memoryKey]*memoryEntry{},
		postings: map[Term][]*memoryEntry{},
	}
	return nil
}

// DeleteIndex implements Store.
func (s *MemoryStore) DeleteIndex(ctx context.Context, name string) error {
	return s.update(ctx, name, func(*memoryIndex) error {
		delete(s.indexes, name)
		return nil
	})
}

// Indexes implements Store.
func (s *MemoryStore) Indexes(ctx context.Context) ([]IndexInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	infos := make([]IndexInfo, 0, len(s.indexes))
	for name, index := range s.indexes {
		infos = append(infos, index.info(name))
	}
	slices.SortFunc(infos, func(a, b IndexInfo) int { return strings.Compare(a.Name, b.Name) })
	return infos, nil
}

// Index implements Store.
func (s *MemoryStore) Index(ctx context.Context, name string) (IndexInfo, error) {
	if err := ctx.Err(); err != nil {
		return IndexInfo{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	index, exists := s.indexes[name]
	if !exists {
		return IndexInfo{}, ErrNotFound
	}
	return index.info(name), nil
}

// SetMarker implements Store.
func (s *MemoryStore) SetMarker(ctx context.Context, name, marker string) error {
	return s.update(ctx, name, func(index *memoryIndex) error {
		index.marker = marker
		return nil
	})
}

// Put implements Store.
func (s *MemoryStore) Put(ctx context.Context, index string, entry Entry, capacity Capacity) error {
	stored := &memoryEntry{
		document: Document{Ref: entry.Document.Ref, Content: bytes.Clone(entry.Document.Content)},
		terms:    slices.Clone(entry.Terms),
	}
	key := keyOf(stored.document.Ref)

	return s.update(ctx, index, func(target *memoryIndex) error {
		old := target.entries[key]
		if !target.fits(old, stored, capacity) {
			return ErrFull
		}
		target.drop(key, old)
		target.add(key, stored)
		return nil
	})
}

// Remove implements Store.
func (s *MemoryStore) Remove(ctx context.Context, index string, ref Ref) error {
	key := keyOf(ref)

	return s.update(ctx, index, func(target *memoryIndex) error {
		old := target.entries[key]
		if old == nil {
			return ErrNotFound
		}
		target.drop(key, old)
		return nil
	})
}

// Refs implements Store.
func (s *MemoryStore) Refs(ctx context.Context, index string, selection Selection, visit func(Ref) bool) error {
	return s.visit(ctx, index, selection, func(entry *memoryEntry) bool { return visit(entry.document.Ref) })
}

// Documents implements Store.
func (s *MemoryStore) Documents(ctx context.Context, index string, selection Selection, visit func(Document) bool) error {
	return s.visit(ctx, index, selection, func(entry *memoryEntry) bool { return visit(entry.document) })
}

// update runs change on an existing index under the write lock.
func (s *MemoryStore) update(ctx context.Context, name string, change func(*memoryIndex) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	index, exists := s.indexes[name]
	if !exists {
		return ErrNotFound
	}
	return change(index)
}

// visit passes the selected entries of an index to visit, in match order.
// The entries are picked under the lock and visited without it, so a slow
// reader never blocks a writer.
func (s *MemoryStore) visit(ctx context.Context, name string, selection Selection, visit func(*memoryEntry) bool) error {
	selected, err := s.selected(ctx, name, selection)
	if err != nil {
		return err
	}
	for _, entry := range selected {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !visit(entry) {
			break
		}
	}
	return nil
}

func (s *MemoryStore) selected(ctx context.Context, name string, selection Selection) ([]*memoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	index, exists := s.indexes[name]
	if !exists {
		return nil, ErrNotFound
	}
	return index.selected(selection), nil
}

func (i *memoryIndex) info(name string) IndexInfo {
	return IndexInfo{Name: name, Marker: i.marker, Documents: len(i.entries), Bytes: i.bytes}
}

// fits reports whether the index stays within capacity when added replaces
// old, which is nil for a new document.
func (i *memoryIndex) fits(old, added *memoryEntry, capacity Capacity) bool {
	documents := len(i.entries)
	size := i.bytes + int64(len(added.document.Content))

	if old == nil {
		documents++
	} else {
		size -= int64(len(old.document.Content))
	}
	return documents <= capacity.Documents && size <= capacity.Bytes
}

func (i *memoryIndex) add(key memoryKey, entry *memoryEntry) {
	i.entries[key] = entry
	i.bytes += int64(len(entry.document.Content))

	for _, term := range entry.terms {
		i.postings[term] = append(i.postings[term], entry)
	}
}

// drop removes an entry, its content and its postings. A nil entry is
// ignored.
func (i *memoryIndex) drop(key memoryKey, entry *memoryEntry) {
	if entry == nil {
		return
	}
	delete(i.entries, key)
	i.bytes -= int64(len(entry.document.Content))

	for _, term := range entry.terms {
		holders := i.postings[term]
		at := slices.Index(holders, entry)

		last := len(holders) - 1
		holders[at] = holders[last]
		holders[last] = nil

		if last == 0 {
			delete(i.postings, term)
		} else {
			i.postings[term] = holders[:last]
		}
	}
}

// selected returns the entries a selection picks, in match order.
func (i *memoryIndex) selected(selection Selection) []*memoryEntry {
	var selected []*memoryEntry

	for _, entry := range i.candidates(selection.Terms) {
		if selection.Matches(entry.document.Ref) && entry.holds(selection.Terms) {
			selected = append(selected, entry)
		}
	}
	slices.SortFunc(selected, func(a, b *memoryEntry) int { return compareRefs(a.document.Ref, b.document.Ref) })
	return selected
}

// candidates returns entries that may hold every term: the shortest posting
// list of the terms, or every entry when there are no terms.
func (i *memoryIndex) candidates(terms []Term) []*memoryEntry {
	if len(terms) == 0 {
		all := make([]*memoryEntry, 0, len(i.entries))
		for _, entry := range i.entries {
			all = append(all, entry)
		}
		return all
	}

	shortest := i.postings[terms[0]]
	for _, term := range terms[1:] {
		if holders := i.postings[term]; len(holders) < len(shortest) {
			shortest = holders
		}
	}
	return shortest
}

func (e *memoryEntry) holds(terms []Term) bool {
	for _, term := range terms {
		if _, found := slices.BinarySearch(e.terms, term); !found {
			return false
		}
	}
	return true
}
