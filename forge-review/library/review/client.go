package review

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Source is an authenticated connection supplied by forge-connector.
type Source struct {
	ID      string
	Session *forgeauth.Session
}

// Client manages reviews. Store and session clients must support concurrent use.
type Client struct {
	store   Store
	sources map[string]*forgeauth.Session
}

// New constructs a client. Zero sources supports supplied diffs independently.
func New(store Store, sources ...Source) (*Client, error) {
	if store == nil {
		return nil, ErrInvalid
	}
	c := &Client{store: store, sources: map[string]*forgeauth.Session{}}
	for _, s := range sources {
		if strings.TrimSpace(s.ID) == "" || s.Session == nil || s.Session.Client == nil || c.sources[s.ID] != nil {
			return nil, ErrInvalid
		}
		switch s.Session.Kind {
		case forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo:
		default:
			return nil, ErrUnsupported
		}
		c.sources[s.ID] = s.Session
	}
	return c, nil
}
func id() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }

// CreateSubject captures a fixed snapshot and returns the existing subject for
// identical input. Connected snapshots never refetch their content on reads.
func (c *Client) CreateSubject(ctx context.Context, in Input) (Subject, error) {
	s := Subject{Kind: in.Kind, Ref: in.Ref, Patch: in.Patch, Context: append([]FileContext(nil), in.Context...), Completeness: "supplied"}
	switch in.Kind {
	case "patch":
		if in.Ref != (Ref{}) || in.From != "" || in.To != "" {
			return Subject{}, ErrInvalid
		}
	case "comparison", "change_request":
		if in.Patch != "" || len(in.Context) > 0 {
			return Subject{}, ErrInvalid
		}
		if err := c.capture(ctx, in, &s); err != nil {
			return Subject{}, err
		}
	default:
		return Subject{}, ErrInvalid
	}
	parsed, err := parsePatch(s.Patch)
	if err != nil {
		return Subject{}, err
	}
	s.Files = parsed.files
	sort.Slice(s.Context, func(i, j int) bool {
		a, b := s.Context[i], s.Context[j]
		if a.Path == b.Path {
			return a.Side < b.Side
		}
		return a.Path < b.Path
	})
	if err = checkContext(s, parsed); err != nil {
		return Subject{}, err
	}
	data, _ := json.Marshal(s)
	hash := sha256.Sum256(data)
	s.ID = hex.EncodeToString(hash[:])
	s.CreatedAt = time.Now().UTC()
	err = c.store.Update(ctx, func(st *State) error {
		if old, ok := st.Subjects[s.ID]; ok {
			s = old
		} else {
			st.Subjects[s.ID] = s
		}
		return nil
	})
	return s, err
}

// GetSubject returns the stored diff, file context and resolved revisions.
func (c *Client) GetSubject(ctx context.Context, subjectID string) (Subject, error) {
	st, err := c.store.Read(ctx)
	if err != nil {
		return Subject{}, err
	}
	s, ok := st.Subjects[subjectID]
	if !ok {
		return Subject{}, ErrNotFound
	}
	return s, nil
}

// RequestReview creates a standalone assignment. It never publishes to a forge;
// use RequestNativeReview explicitly for native reviewer requests.
func (c *Client) RequestReview(ctx context.Context, subjectID, reviewer string) (Review, error) {
	if strings.TrimSpace(reviewer) == "" {
		return Review{}, ErrInvalid
	}
	r := Review{ID: id(), SubjectID: subjectID, Reviewer: reviewer, Storage: "local", Status: "requested", CreatedAt: time.Now().UTC()}
	err := c.store.Update(ctx, func(st *State) error {
		if _, ok := st.Subjects[subjectID]; !ok {
			return ErrNotFound
		}
		st.Reviews[r.ID] = r
		return nil
	})
	return r, err
}

// GetReview retrieves an assignment, assessment and all its threads.
func (c *Client) GetReview(ctx context.Context, reviewID string) (Review, error) {
	st, err := c.store.Read(ctx)
	if err != nil {
		return Review{}, err
	}
	r, ok := st.Reviews[reviewID]
	if !ok {
		return Review{}, ErrNotFound
	}
	return r, nil
}

// ListReviews filters local assignments. Empty filters match every value.
func (c *Client) ListReviews(ctx context.Context, subjectID, reviewer, status string) ([]Review, error) {
	if status != "" && status != "requested" && !assessment(status) {
		return nil, ErrInvalid
	}
	st, err := c.store.Read(ctx)
	if err != nil {
		return nil, err
	}
	out := []Review{}
	for _, r := range st.Reviews {
		if (subjectID == "" || r.SubjectID == subjectID) && (reviewer == "" || r.Reviewer == reviewer) && (status == "" || r.Status == status) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (c *Client) update(ctx context.Context, rid string, fn func(*Review, Subject) error) (Review, error) {
	var out Review
	err := c.store.Update(ctx, func(st *State) error {
		r, ok := st.Reviews[rid]
		if !ok {
			return ErrNotFound
		}
		if err := fn(&r, st.Subjects[r.SubjectID]); err != nil {
			return err
		}
		st.Reviews[rid] = r
		out = r
		return nil
	})
	return out, err
}

// AddComment opens a thread on a requested review. Nil anchor is general
// feedback. Anchors are validated using only the stored snapshot.
func (c *Client) AddComment(ctx context.Context, rid, author, body string, a *Anchor) (Review, error) {
	if strings.TrimSpace(author) == "" || strings.TrimSpace(body) == "" {
		return Review{}, ErrInvalid
	}
	return c.update(ctx, rid, func(r *Review, s Subject) error {
		if r.Status != "requested" {
			return ErrConflict
		}
		if err := ValidateAnchor(s, a); err != nil {
			return err
		}
		var anchor *Anchor
		if a != nil {
			v := *a
			anchor = &v
		}
		r.Threads = append(r.Threads, Thread{ID: id(), SubjectID: s.ID, Anchor: anchor, Comments: []Comment{{ID: id(), Author: author, Body: body, CreatedAt: time.Now().UTC()}}})
		return nil
	})
}

// Reply appends to an existing thread, including after submission/resolution.
func (c *Client) Reply(ctx context.Context, rid, tid, author, body string) (Review, error) {
	if strings.TrimSpace(author) == "" || strings.TrimSpace(body) == "" {
		return Review{}, ErrInvalid
	}
	return c.update(ctx, rid, func(r *Review, _ Subject) error {
		for i := range r.Threads {
			if r.Threads[i].ID == tid {
				r.Threads[i].Comments = append(r.Threads[i].Comments, Comment{ID: id(), Author: author, Body: body, CreatedAt: time.Now().UTC()})
				return nil
			}
		}
		return ErrNotFound
	})
}

// Resolve resolves or reopens a thread without changing its location.
func (c *Client) Resolve(ctx context.Context, rid, tid string, resolved bool) (Review, error) {
	return c.update(ctx, rid, func(r *Review, _ Subject) error {
		for i := range r.Threads {
			if r.Threads[i].ID == tid {
				r.Threads[i].Resolved = resolved
				return nil
			}
		}
		return ErrNotFound
	})
}
func assessment(s string) bool {
	return s == "commented" || s == "approved" || s == "changes_requested"
}

// Submit records the assigned reviewer's final assessment. Repeated identical
// submissions are idempotent; changing a submitted assessment is a conflict.
func (c *Client) Submit(ctx context.Context, rid, status, body string) (Review, error) {
	if !assessment(status) {
		return Review{}, ErrInvalid
	}
	return c.update(ctx, rid, func(r *Review, _ Subject) error {
		if r.Status != "requested" {
			if r.Status == status && r.Body == body {
				return nil
			}
			return ErrConflict
		}
		if status != "approved" && strings.TrimSpace(body) == "" && len(r.Threads) == 0 {
			return ErrInvalid
		}
		r.Status = status
		r.Body = body
		now := time.Now().UTC()
		r.SubmittedAt = &now
		return nil
	})
}
