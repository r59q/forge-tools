package issue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-search/library/search"
)

// SyncResult reports what a sync changed.
type SyncResult struct {
	// SyncedAt is when the listing the index now holds was read. It is zero
	// unless the sync completed.
	SyncedAt time.Time `json:"syncedAt,omitzero"`

	// Added, Replaced and Removed count the issues written; Unchanged
	// counts those left as they were.
	Added     int `json:"added"`
	Replaced  int `json:"replaced"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`

	// Issues counts the issues left out, which the index does not hold.
	Issues State `json:"issues"`
}

// Sync brings the issue documents of one repository in an index to the
// open and closed issues reader lists for it, then records when that
// listing was read. repository.Source is required and names every
// document. Documents of other repositories and kinds are not touched. The
// index must exist. Zero limits use DefaultSyncLimits.
//
// The first sync adds every issue. Later syncs compare each issue with the
// version in the index and only add, replace and remove what changed,
// removals first. A search during a sync sees each issue entirely old or
// entirely new.
//
// A sync first clears the record of the last sync, and records the new one
// only when it completes, so LastSynced never names a listing the index
// does not hold. A sync that fails or is canceled returns what it changed so
// far, and the next sync corrects the index. Syncs of one repository into
// one index must not run at the same time.
//
// A reader that fails returns its error. More than Limits.Issues return
// search.ErrTooLarge before the index changes. Issues over a size limit are
// left out, and removed from the index.
func Sync(
	ctx context.Context, reader Reader, engine *search.Engine, index string, repository issues.RepositoryRef, limits Limits,
) (SyncResult, error) {
	limits = limits.withDefaults(DefaultSyncLimits())
	if err := checkRepository(repository); err != nil {
		return SyncResult{}, err
	}
	if _, err := engine.Index(ctx, index); err != nil {
		return SyncResult{}, err
	}
	if err := unmark(ctx, engine, index, repository); err != nil {
		return SyncResult{}, err
	}

	started := time.Now().UTC()
	list, err := listIssues(ctx, reader, repository, limits)
	if err != nil {
		return SyncResult{}, err
	}

	at := homeOf(repository)
	documents, left, err := mapIssues(list, func(issues.Issue) home { return at }, Filter{}, limits)
	if err != nil {
		return SyncResult{}, err
	}

	stored, err := engine.Documents(ctx, index, selection(Filter{Source: repository.Source, Repository: repository.Repository}))
	if err != nil {
		return SyncResult{}, err
	}

	s := syncer{engine: engine, index: index, result: SyncResult{Issues: left}}
	if err := s.run(ctx, documents, stored); err != nil {
		return s.result, err
	}
	if err := mark(ctx, engine, index, repository, started); err != nil {
		return s.result, err
	}

	s.result.SyncedAt = started
	return s.result, nil
}

// LastSynced returns when the listing of the last completed sync of a
// repository into an index was read. It is zero when there was none, or
// while a sync changes the repository.
func LastSynced(ctx context.Context, engine *search.Engine, index string, repository issues.RepositoryRef) (time.Time, error) {
	if err := checkRepository(repository); err != nil {
		return time.Time{}, err
	}

	record := syncRecord(repository)
	refs, err := engine.Documents(ctx, index, search.Filter{Kind: record.Kind, Source: record.Source, ID: record.ID})
	if err != nil || len(refs) == 0 {
		return time.Time{}, err
	}

	synced, err := time.Parse(time.RFC3339Nano, refs[0].Version)
	if err != nil {
		return time.Time{}, fmt.Errorf("sync record: %w", search.ErrInvalid)
	}
	return synced, nil
}

// syncRecord is the document that records the last sync of a repository.
// Its version is the time; it has no content.
func syncRecord(repository issues.RepositoryRef) search.Ref {
	return search.Ref{Kind: KindSync, Source: repository.Source, ID: repository.Repository}
}

func unmark(ctx context.Context, engine *search.Engine, index string, repository issues.RepositoryRef) error {
	err := engine.Remove(ctx, index, syncRecord(repository))
	if errors.Is(err, search.ErrNotFound) {
		return nil
	}
	return err
}

func mark(ctx context.Context, engine *search.Engine, index string, repository issues.RepositoryRef, synced time.Time) error {
	record := syncRecord(repository)
	record.Version = synced.Format(time.RFC3339Nano)

	return engine.Put(ctx, index, search.Document{Ref: record, Content: []byte{}})
}

// syncer applies one sync to an index.
type syncer struct {
	engine *search.Engine
	index  string
	result SyncResult
}

func (s *syncer) run(ctx context.Context, documents []search.Document, stored []search.Ref) error {
	versions := make(map[string]string, len(stored))
	for _, ref := range stored {
		versions[ref.ID] = ref.Version
	}

	listed := make(map[string]bool, len(documents))
	for _, document := range documents {
		listed[document.ID] = true
	}

	for _, ref := range stored {
		if listed[ref.ID] {
			continue
		}
		if err := s.remove(ctx, ref); err != nil {
			return err
		}
	}

	for _, document := range documents {
		version, held := versions[document.ID]
		if err := s.put(ctx, document, version, held); err != nil {
			return err
		}
	}
	return nil
}

// put writes an issue whose document differs from the version the index
// holds.
func (s *syncer) put(ctx context.Context, document search.Document, version string, held bool) error {
	if held && document.Version == version {
		s.result.Unchanged++
		return nil
	}

	if err := s.engine.Put(ctx, s.index, document); err != nil {
		return fmt.Errorf("issue %s: %w", numberOf(document.ID), err)
	}

	if held {
		s.result.Replaced++
	} else {
		s.result.Added++
	}
	return nil
}

func (s *syncer) remove(ctx context.Context, ref search.Ref) error {
	err := s.engine.Remove(ctx, s.index, ref)
	if err != nil && !errors.Is(err, search.ErrNotFound) {
		return fmt.Errorf("issue %s: %w", numberOf(ref.ID), err)
	}

	s.result.Removed++
	return nil
}

// numberOf names the issue of a document ID in an error.
func numberOf(id string) string {
	if _, number, ok := parseID(id); ok {
		return fmt.Sprint(number)
	}
	return "document"
}
