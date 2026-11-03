package review

import (
	"context"
	"errors"
	"sync"
	"testing"
)

const patch = "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -2,2 +2,3 @@\n same\n-old\n+new\n+more\n"

func local(t *testing.T) (*Client, Subject) {
	t.Helper()
	c, err := New(&MemoryStore{})
	if err != nil {
		t.Fatal(err)
	}

	s, err := c.CreateSubject(context.Background(), Input{Kind: "patch", Patch: patch})
	if err != nil {
		t.Fatal(err)
	}

	return c, s
}

func TestSnapshotIdentityAndIsolation(t *testing.T) {
	ctx := context.Background()
	c, s := local(t)
	again, err := c.CreateSubject(ctx, Input{Kind: "patch", Patch: patch})
	if err != nil || s.ID != again.ID || !s.CreatedAt.Equal(again.CreatedAt) {
		t.Fatalf("dedup: %+v %v", again, err)
	}

	changed, err := c.CreateSubject(ctx, Input{
		Kind:  "patch",
		Patch: patch,
		Context: []FileContext{{
			Path:    "main.go",
			Side:    "new",
			Content: "first\nsame\nnew\nmore\n",
		}},
	})
	if err != nil || s.ID == changed.ID {
		t.Fatalf("context identity: %v", err)
	}

	s.Files[0].Path = "changed"
	stored, _ := c.GetSubject(ctx, s.ID)
	if stored.Files[0].Path != "main.go" {
		t.Fatal("mutable snapshot")
	}

	_, err = c.CreateSubject(ctx, Input{
		Kind:  "patch",
		Patch: patch,
		Context: []FileContext{{
			Path:    "main.go",
			Side:    "new",
			Content: "inconsistent",
		}},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("contradictory context: %v", err)
	}
}

func TestAnchors(t *testing.T) {
	_, s := local(t)
	for _, tc := range []struct {
		name  string
		a     *Anchor
		valid bool
	}{
		{"general", nil, true}, {"file", &Anchor{Path: "main.go", Side: "new"}, true},
		{"old", &Anchor{"main.go", "old", 2, 3}, true}, {"new", &Anchor{"main.go", "new", 2, 4}, true},
		{"outside", &Anchor{"main.go", "new", 1, 4}, false}, {"deleted side", &Anchor{"main.go", "old", 4, 4}, false},
		{"missing", &Anchor{"other", "new", 2, 2}, false}, {"side", &Anchor{"main.go", "both", 2, 2}, false},
		{"backwards", &Anchor{"main.go", "new", 4, 2}, false}, {"zero", &Anchor{"main.go", "new", 0, 2}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAnchor(s, tc.a)
			if (err == nil) != tc.valid {
				t.Fatalf("%v", err)
			}
		})
	}

	s.Context = []FileContext{{
		Path:    "main.go",
		Side:    "new",
		Content: "first\nsame\nnew\nmore\n",
	}}
	if err := ValidateAnchor(s, &Anchor{"main.go", "new", 1, 4}); err != nil {
		t.Fatal(err)
	}
}

func TestPatchFormsAndMalformedHunks(t *testing.T) {
	for _, p := range []string{
		"", "--- /dev/null\n+++ b/a\n@@ -0,0 +1 @@\n+new\n",
		"--- a/a\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n\\ No newline at end of file\n",
		"diff --git \"a/a b\" \"b/c d\"\nsimilarity index 100%\nrename from a b\nrename to c d\n",
		"diff --git a/b b/b\nBinary files a/b and b/b differ\n",
		"--- a/a\n+++ b/a\n@@ -1 +1 @@\n-a\n+b\n@@ -10 +10 @@\n-x\n+y\n",
	} {
		if _, err := parsePatch(p); err != nil {
			t.Errorf("valid patch %q: %v", p, err)
		}
	}

	for _, p := range []string{
		"garbage", "--- a/a\n+++ b/a\n@@ -1,2 +1 @@\n-a\n+b\n", "@@ -1 +1 @@\n-a\n+b\n",
		"--- a/../a\n+++ b/a\n", "--- a/a\n+++ b/a\n@@ -1 +1 @@\n-a\n+b\n+extra\n",
		"--- a/a\n+++ b/a\n@@ -1 +1 @@\n-a\n+b\n@@ -1 +1 @@\n-a\n+b\n",
		"--- a/a\n+++ b/a\n@@ -0 +1 @@\n-a\n+b\n",
	} {
		if _, err := parsePatch(p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
}

func TestReviewLifecycleAndThreads(t *testing.T) {
	ctx := context.Background()
	c, s := local(t)
	r, err := c.RequestReview(ctx, s.ID, "robot")
	if err != nil {
		t.Fatal(err)
	}

	r, err = c.AddComment(ctx, r.ID, "robot", "please simplify", &Anchor{"main.go", "new", 3, 4})
	if err != nil {
		t.Fatal(err)
	}

	tid := r.Threads[0].ID
	r, err = c.Submit(ctx, r.ID, "changes_requested", "please revise")
	if err != nil || r.SubmittedAt == nil {
		t.Fatal(err)
	}
	if _, err = c.AddComment(ctx, r.ID, "robot", "new thread", nil); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = c.Submit(ctx, r.ID, "approved", ""); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = c.Submit(ctx, r.ID, "changes_requested", "please revise"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Reply(ctx, r.ID, tid, "human", "done"); err != nil {
		t.Fatal(err)
	}

	r, err = c.Resolve(ctx, r.ID, tid, true)
	if err != nil ||
		!r.Threads[0].Resolved || len(r.Threads[0].Comments) != 2 || r.Threads[0].SubjectID != s.ID || r.Threads[0].Anchor.Start != 3 {
		t.Fatalf("thread: %+v %v", r, err)
	}

	r, err = c.Resolve(ctx, r.ID, tid, false)
	if err != nil || r.Threads[0].Resolved {
		t.Fatal(err)
	}

	items, err := c.ListReviews(ctx, s.ID, "robot", "changes_requested")
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}

	approved, _ := c.RequestReview(ctx, s.ID, "human")
	if _, err = c.Submit(ctx, approved.ID, "approved", ""); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentRepliesAndRollback(t *testing.T) {
	ctx := context.Background()
	c, s := local(t)
	r, _ := c.RequestReview(ctx, s.ID, "reviewer")
	r, _ = c.AddComment(ctx, r.ID, "reviewer", "start", nil)
	tid := r.Threads[0].ID
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Reply(ctx, r.ID, tid, "other", "reply"); err != nil {
				t.Error(err)
			}
		}()
	}

	wg.Wait()
	got, _ := c.GetReview(ctx, r.ID)
	if len(got.Threads[0].Comments) != 21 {
		t.Fatal("lost replies")
	}

	err := c.store.Update(ctx, func(s *State) error {
		delete(s.Reviews, r.ID)
		return ErrInvalid
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = c.GetReview(ctx, r.ID); err != nil {
		t.Fatal("rollback failed")
	}
}

func TestUnsupportedLocalNativeOperations(t *testing.T) {
	c, s := local(t)
	if _, err := c.SubmitNative(context.Background(), s.ID, "approved", ""); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestReversedHunksAndUnavailableLines(t *testing.T) {
	p := "--- a/a\n+++ b/a\n@@ -10 +10 @@\n-old\n+new\n@@ -1 +1 @@\n-x\n+y\n"
	if _, err := parsePatch(p); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted reversed hunks", err)
	}

	p = "--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n@@ -10 +10 @@\n-x\n+y\n"
	if err := ValidateAnchor(Subject{Patch: p}, &Anchor{
		Path:  "a",
		Side:  "new",
		Start: 1,
		End:   10,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted gap", err)
	}

	p = "diff --git a/a b/a\nBinary files a/a and b/a differ\n"
	if err := ValidateAnchor(Subject{Patch: p}, &Anchor{
		Path:  "a",
		Side:  "new",
		Start: 1,
		End:   1,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted binary line", err)
	}
	if err := ValidateAnchor(Subject{Patch: p}, &Anchor{Path: "a", Side: "new"}); err != nil {
		t.Fatal(err)
	}
}
