package checkouts

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

func TestIsolatedPinRecoveryCheckpoints(t *testing.T) {
	for _, tc := range []struct {
		name          string
		state         string
		clearPin      bool
		clearRecord   bool
		clearCopy     bool
		clearRevision bool
	}{
		{name: "journal before pin", state: Creating, clearPin: true, clearCopy: true, clearRevision: true},
		{name: "pin before revision record", state: Creating, clearCopy: true, clearRevision: true},
		{name: "revision before copy", state: Creating, clearCopy: true},
		{name: "copy before ready", state: Creating},
		{name: "removal before unpin", state: Removing, clearCopy: true},
		{name: "unpin before forgetting", state: Removing, clearPin: true, clearCopy: true},
		{name: "orphan pin", clearRecord: true, clearCopy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, store, w, cache := isolated(t)
			ctx := t.Context()
			if tc.clearCopy {
				if err := os.RemoveAll(w.Location); err != nil {
					t.Fatal(err)
				}
			}
			if tc.clearPin {
				command(t, cache, "update-ref", "-d", "refs/checkouts/"+w.ID)
			}
			if err := store.Update(ctx, func(s *State) error {
				r := s.Checkouts[w.ID]
				r.Checkout.State = tc.state
				if tc.clearRevision {
					r.Checkout.Revision = ""
				}
				s.Checkouts[w.ID] = r
				if tc.clearRecord {
					delete(s.Checkouts, w.ID)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			next, err := New(ctx, m.root, store, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			wantPin := w.Revision
			if tc.clearRecord || tc.clearRevision {
				wantPin = ""
			}
			if got := command(t, cache, "for-each-ref", "--format=%(objectname)", "refs/checkouts/"+w.ID); got != wantPin {
				t.Fatal("recovered pin", got, "want", wantPin)
			}
			if !tc.clearRecord {
				got, err := next.Get(ctx, w.ID)
				if err != nil || got.State != Failed {
					t.Fatal(got, err)
				}
				if err = next.Remove(ctx, w.ID, false); err != nil {
					t.Fatal(err)
				}
			}
			if command(t, cache, "for-each-ref", "refs/checkouts/") != "" {
				t.Fatal("pin leaked")
			}
		})
	}
}

func TestIsolatedFailedFetchIsRemovable(t *testing.T) {
	m, _, in := connectedManager(t)
	m.resolve = func(context.Context, string) (*forgeauth.GitRemote, error) {
		return nil, ErrNotFound
	}
	if _, err := m.Create(t.Context(), in); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	all, err := m.List(t.Context())
	if err != nil || len(all) != 1 || all[0].State != Failed {
		t.Fatal(all, err)
	}
	if err = m.Remove(t.Context(), all[0].ID, false); err != nil {
		t.Fatal(err)
	}
}
