package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-review/library/review"
)

func TestRestartAndRollback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}

	c, _ := review.New(store)
	s, err := c.CreateSubject(ctx, review.Input{Kind: "patch", Patch: "--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n"})
	if err != nil {
		t.Fatal(err)
	}

	r, _ := c.RequestReview(ctx, s.ID, "reviewer")
	r, err = c.AddComment(ctx, r.ID, "reviewer", "check", &review.Anchor{
		Path:  "a",
		Side:  "new",
		Start: 1,
		End:   1,
	})
	if err != nil {
		t.Fatal(err)
	}

	tid := r.Threads[0].ID
	if _, err = c.Reply(ctx, r.ID, tid, "author", "done"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Resolve(ctx, r.ID, tid, true); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Submit(ctx, r.ID, "approved", ""); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}

	next, _ := review.New(reopened)
	got, err := next.GetReview(ctx, r.ID)
	if err != nil || got.Status != "approved" || !got.Threads[0].Resolved || len(got.Threads[0].Comments) != 2 {
		t.Fatalf("%+v %v", got, err)
	}

	snapshot, _ := next.GetSubject(ctx, s.ID)
	if snapshot.Patch != s.Patch || snapshot.ID != s.ID {
		t.Fatal("snapshot changed")
	}

	err = reopened.Update(ctx, func(st *review.State) error {
		delete(st.Reviews, r.ID)
		return review.ErrInvalid
	})
	if !errors.Is(err, review.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = next.GetReview(ctx, r.ID); err != nil {
		t.Fatal("rollback failed")
	}

	info, _ := os.Stat(filepath.Join(dir, "reviews.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("data permissions", info.Mode())
	}
}

func TestConcurrentHandlesAndCancellation(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(context.Background(), dir)
	b, _ := Open(context.Background(), dir)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := a
			if i%2 == 0 {
				f = b
			}

			err := f.Update(context.Background(), func(st *review.State) error {
				st.Reviews[time.Now().String()+string(rune(i))] = review.Review{}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}

	wg.Wait()
	s, _ := a.Read(context.Background())
	if len(s.Reviews) != 20 {
		t.Fatal("lost updates", len(s.Reviews))
	}

	lock, err := a.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	defer unlock(lock)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = b.Read(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestCorruptStoreIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "reviews.json")
	bad := []byte(`{"version":1`)
	if err := os.WriteFile(file, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), dir); err == nil {
		t.Fatal("accepted corrupt data")
	}

	got, _ := os.ReadFile(file)
	if string(got) != string(bad) {
		t.Fatal("overwrote corrupt data")
	}
}
