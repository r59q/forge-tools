package api

import (
	"context"
	"errors"
	"os"
	"time"
)

// MaxTemporaryCheckouts is the most checkouts the server holds at once for
// IndexRepository and SearchRepository calls. Further calls fail with
// RESOURCE_EXHAUSTED instead of waiting.
const MaxTemporaryCheckouts = 4

// Time limits of removing temporary checkouts: one removal, which runs even
// after the call's context ends, and one sweep of journaled leftovers.
const (
	removeTimeout = 30 * time.Second
	sweepTimeout  = 10 * time.Second
)

var errCheckoutsBusy = errors.New("api: too many temporary checkouts")

// checkOut creates a checkout of target for one call, journals it and opens
// its directory. done closes the directory and removes the checkout, also
// when ctx has ended; a checkout it cannot remove stays journaled for the
// next sweep.
func (s *Server) checkOut(ctx context.Context, target Target) (c Checkout, root *os.Root, done func(), err error) {
	select {
	case s.slots <- struct{}{}:
	default:
		return Checkout{}, nil, nil, errCheckoutsBusy
	}
	s.Sweep(ctx)

	c, err = s.config.Checkouts.Create(ctx, target)
	if err != nil {
		<-s.slots
		return Checkout{}, nil, nil, createError(ctx, err)
	}

	done = func() {
		if root != nil {
			_ = root.Close()
		}
		s.remove(ctx, c.ID)
		<-s.slots
	}
	if err := s.track(c.ID); err != nil {
		done()
		return Checkout{}, nil, nil, err
	}

	switch {
	case c.State != "ready":
		err = errNotReady
	default:
		root, err = s.config.Root.open(c)
	}
	if err != nil {
		done()
		return Checkout{}, nil, nil, err
	}
	return c, root, done, nil
}

// createError maps an error of Checkouts.Create.
func createError(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrRejected):
		return err
	}
	return errUnavailable
}

// track marks a new checkout as in use and journals it.
func (s *Server) track(id string) error {
	if !validCheckoutID(id) {
		return errUnavailable
	}

	s.mu.Lock()
	s.live[id] = true
	s.mu.Unlock()

	if err := s.config.Journal.Add(id); err != nil {
		return errJournal
	}
	return nil
}

// remove removes a temporary checkout and, once it is gone, forgets it.
func (s *Server) remove(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()

	err := s.config.Checkouts.Remove(ctx, id)
	if (err == nil || errors.Is(err, ErrNotFound)) && validCheckoutID(id) {
		_ = s.config.Journal.Remove(id)
	}

	s.mu.Lock()
	delete(s.live, id)
	s.mu.Unlock()
}

// Sweep removes the journaled checkouts no call is using: those a crash or
// a failed removal left. It gives up after a short time and reports
// nothing; the next sweep tries again. Calls that create a checkout sweep
// first, and a service should sweep when it starts.
func (s *Server) Sweep(ctx context.Context) {
	if !s.sweeping.TryLock() {
		return
	}
	defer s.sweeping.Unlock()

	ids, err := s.config.Journal.List()
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sweepTimeout)
	defer cancel()
	for _, id := range ids {
		s.mu.Lock()
		live := s.live[id]
		s.mu.Unlock()
		if live || ctx.Err() != nil {
			continue
		}

		err := s.config.Checkouts.Remove(ctx, id)
		if err == nil || errors.Is(err, ErrNotFound) {
			_ = s.config.Journal.Remove(id)
		}
	}
}
