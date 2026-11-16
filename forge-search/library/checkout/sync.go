package checkout

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// SyncResult reports what a sync changed. Other aggregates, such as symbols,
// can follow the same change set.
type SyncResult struct {
	// Revision is the revision recorded with the index. It is empty unless
	// the sync completed.
	Revision string `json:"revision,omitempty"`

	// Added, Replaced and Removed count the documents written; Unchanged
	// counts those left as they were.
	Added     int `json:"added"`
	Replaced  int `json:"replaced"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`

	// ChangedPaths lists the added and replaced files, in path order.
	ChangedPaths []string `json:"changedPaths"`

	// RemovedPaths lists the files no longer in the index, in path order.
	RemovedPaths []string `json:"removedPaths"`

	// Files counts the files left out, which the index does not hold.
	Files State `json:"files"`
}

// Sync brings the code documents of source in an index to the content of the
// directory tree below root, then records revision as the index marker.
// Documents of other kinds and sources are not touched. The index must
// exist. Zero limits use DefaultSyncLimits.
//
// The first sync adds every file. Later syncs compare each file with the
// version in the index, a hash of its content, and only add, replace and
// remove what changed. Removals come first, so that they free room for
// what is added.
//
// A search during a sync sees each document entirely old or entirely new.
// Before its first change, a sync clears the marker; it records revision
// only when it completes. A marker that is set therefore names the revision
// the index holds. A sync that fails or is canceled leaves an index that the
// next sync corrects, and returns what it changed so far. Syncs of one index
// must not run at the same time.
//
// Too many entries, too deep a directory or more than Limits.TotalBytes
// return search.ErrTooLarge before the index changes. A file that changes
// while it is read returns ErrChanged. Files over Limits.FileBytes are left
// out, and removed from the index, as Read does.
func Sync(ctx context.Context, root *os.Root, engine *search.Engine, index, revision string, options Options) (SyncResult, error) {
	limits := options.Limits.withDefaults(DefaultSyncLimits())
	if err := checkSync(options, revision); err != nil {
		return SyncResult{}, err
	}

	info, err := engine.Index(ctx, index)
	if err != nil {
		return SyncResult{}, err
	}

	listed, err := list(ctx, root, limits, "")
	if err != nil {
		return SyncResult{}, err
	}
	if listed.bytes() > limits.TotalBytes {
		return SyncResult{}, fmt.Errorf("files: %w", search.ErrTooLarge)
	}

	stored, err := engine.Documents(ctx, index, filter(options.Source))
	if err != nil {
		return SyncResult{}, err
	}

	s := newSyncer(root, engine, index, options.Source, info.Marker)
	s.result.Files = listed.state

	if err := s.run(ctx, listed, stored); err != nil {
		return s.result, err
	}
	if err := engine.SetMarker(ctx, index, revision); err != nil {
		return s.result, err
	}

	s.result.Revision = revision
	return s.result, nil
}

// Revision returns the revision the last completed sync recorded with an
// index, or "" while a sync changes it.
func Revision(ctx context.Context, engine *search.Engine, index string) (string, error) {
	info, err := engine.Index(ctx, index)
	if err != nil {
		return "", err
	}
	return info.Marker, nil
}

func checkSync(options Options, revision string) error {
	if !validName(options.Source) {
		return fmt.Errorf("source: %w", search.ErrInvalid)
	}
	if options.Prefix != "" {
		return fmt.Errorf("prefix: %w", search.ErrInvalid)
	}
	validRevision := len(revision) <= search.MaxMarkerBytes && validName(revision)
	if !validRevision {
		return fmt.Errorf("revision: %w", search.ErrInvalid)
	}
	return nil
}

// syncer applies one sync to an index.
type syncer struct {
	root   *os.Root
	engine *search.Engine
	index  string
	source string

	// marked is set while the index marker may name an earlier revision.
	marked bool

	result SyncResult
}

func newSyncer(root *os.Root, engine *search.Engine, index, source, marker string) *syncer {
	return &syncer{
		root:   root,
		engine: engine,
		index:  index,
		source: source,
		marked: marker != "",
		result: SyncResult{ChangedPaths: []string{}, RemovedPaths: []string{}},
	}
}

func (s *syncer) run(ctx context.Context, listed listing, stored []search.Ref) error {
	versions := make(map[string]string, len(stored))
	for _, ref := range stored {
		versions[ref.ID] = ref.Version
	}

	present := make(map[string]bool, len(listed.files))
	for _, file := range listed.files {
		present[file.path] = true
	}

	for _, ref := range stored {
		if present[ref.ID] {
			continue
		}
		if err := s.remove(ctx, ref.ID); err != nil {
			return err
		}
	}

	for _, file := range listed.files {
		version, held := versions[file.path]
		if err := s.file(ctx, file, version, held); err != nil {
			return err
		}
	}
	return nil
}

// file puts a file whose content differs from the version the index holds.
func (s *syncer) file(ctx context.Context, listed file, version string, held bool) error {
	content, err := readFile(ctx, s.root, listed)
	if err != nil {
		return err
	}

	next := document(s.source, listed, content)
	if held && next.Version == version {
		s.result.Unchanged++
		return nil
	}

	if err := s.unmark(ctx); err != nil {
		return err
	}
	if err := s.engine.Put(ctx, s.index, next); err != nil {
		return fmt.Errorf("file %q: %w", listed.path, err)
	}

	if held {
		s.result.Replaced++
	} else {
		s.result.Added++
	}
	s.result.ChangedPaths = append(s.result.ChangedPaths, listed.path)
	return nil
}

func (s *syncer) remove(ctx context.Context, path string) error {
	if err := s.unmark(ctx); err != nil {
		return err
	}

	ref := search.Ref{Kind: search.KindCode, Source: s.source, ID: path}
	err := s.engine.Remove(ctx, s.index, ref)
	if err != nil && !errors.Is(err, search.ErrNotFound) {
		return fmt.Errorf("file %q: %w", path, err)
	}

	s.result.Removed++
	s.result.RemovedPaths = append(s.result.RemovedPaths, path)
	return nil
}

// unmark clears the marker before the first change, so that it never names
// a revision the index no longer holds.
func (s *syncer) unmark(ctx context.Context) error {
	if !s.marked {
		return nil
	}
	if err := s.engine.SetMarker(ctx, s.index, ""); err != nil {
		return err
	}
	s.marked = false
	return nil
}
