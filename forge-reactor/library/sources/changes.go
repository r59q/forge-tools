package sources

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

// Create stores a new source with a new secret. With Register, it then
// creates the webhook on every repository; if any fails, the hooks made so
// far and the source are removed again.
func (m *Manager) Create(ctx context.Context, req Request) (Source, error) {
	src, err := m.check(req)
	if err != nil {
		return Source{}, err
	}
	unlock, err := m.mutex(ctx)
	if err != nil {
		return Source{}, err
	}
	defer unlock()

	// Store first, so the forge's first delivery finds the source.
	if err := m.Store.Update(ctx, func(all map[string]Source) error { return add(all, src) }); err != nil {
		return Source{}, err
	}
	if !req.Register {
		return src, nil
	}

	src.Hooks, err = m.register(ctx, src)
	if err == nil {
		err = m.Store.Update(ctx, func(all map[string]Source) error { all[src.ID] = src; return nil })
	}
	if err != nil {
		m.undo(context.WithoutCancel(ctx), src)

		return Source{}, err
	}

	return src, nil
}

// add stores src unless its ID is taken.
func add(all map[string]Source, src Source) error {
	if _, ok := all[src.ID]; ok {
		return reactor.ErrExists
	}
	all[src.ID] = src

	return nil
}

// undo removes a source whose registration failed.
func (m *Manager) undo(ctx context.Context, src Source) {
	_ = m.unregister(ctx, src)
	_ = m.Store.Update(ctx, func(all map[string]Source) error { delete(all, src.ID); return nil })
}

// Rotate gives a source a new secret and updates its registered hooks. It
// returns the repositories whose hooks could not be updated; rotating
// again retries them.
func (m *Manager) Rotate(ctx context.Context, id string) (Source, []string, error) {
	unlock, err := m.mutex(ctx)
	if err != nil {
		return Source{}, nil, err
	}
	defer unlock()

	var src Source
	err = m.Store.Update(ctx, func(all map[string]Source) error {
		found, ok := all[id]
		if !ok {
			return reactor.ErrNotFound
		}
		found.Secret = newSecret()
		all[id], src = found, found

		return nil
	})
	if err != nil {
		return Source{}, nil, err
	}

	return src, m.update(ctx, src), nil
}

// Delete removes a source and its registered hooks. It returns the
// repositories whose hooks could not be removed; the source is gone either
// way, so their deliveries fail with not found.
func (m *Manager) Delete(ctx context.Context, id string) ([]string, error) {
	unlock, err := m.mutex(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	var src Source
	err = m.Store.Update(ctx, func(all map[string]Source) error {
		found, ok := all[id]
		if !ok {
			return reactor.ErrNotFound
		}
		delete(all, id)
		src = found

		return nil
	})
	if err != nil {
		return nil, err
	}

	return m.unregister(ctx, src), nil
}

// check validates req and returns the new source.
func (m *Manager) check(req Request) (Source, error) {
	repos, err := repositories(req.Repositories)
	switch {
	case err != nil:
		return Source{}, err
	case !reactor.ValidID(req.ID) || slices.Contains(m.Reserved, req.ID):
		return Source{}, fmt.Errorf("%w: source ID", reactor.ErrInvalid)
	case req.Forge != forgeauth.GitHub && req.Forge != forgeauth.Forgejo:
		return Source{}, fmt.Errorf("%w: forge", reactor.ErrInvalid)
	case len(req.Connection) > 128:
		return Source{}, fmt.Errorf("%w: connection", reactor.ErrInvalid)
	}

	if req.Register {
		if err := m.checkRegister(req.Connection, repos); err != nil {
			return Source{}, err
		}
	}

	return Source{
		ID:           req.ID,
		Forge:        req.Forge,
		Connection:   req.Connection,
		Repositories: repos,
		Secret:       newSecret(),
		CreatedAt:    m.now(),
	}, nil
}

// checkRegister checks what registration needs.
func (m *Manager) checkRegister(connection string, repos []string) error {
	switch {
	case !m.CanRegister():
		return ErrNotConfigured
	case connection == "" || len(repos) == 0:
		return fmt.Errorf("%w: registration needs a connection and repositories", reactor.ErrInvalid)
	}

	return nil
}

// repositories checks and deduplicates owner/name paths, ignoring case.
func repositories(in []string) ([]string, error) {
	if len(in) > maxRepositories {
		return nil, fmt.Errorf("%w: too many repositories", reactor.ErrInvalid)
	}

	var out []string
	for _, r := range in {
		if !validRepository(r) {
			return nil, fmt.Errorf("%w: repository %q", reactor.ErrInvalid, r)
		}
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, r) }) {
			out = append(out, r)
		}
	}

	return out, nil
}

// validRepository reports an owner/name path.
func validRepository(r string) bool {
	owner, name, ok := strings.Cut(r, "/")

	return ok && validSegment(owner) && validSegment(name)
}

// validSegment reports an owner or repository name, as both forges allow.
func validSegment(s string) bool {
	if s == "" || len(s) > 100 || s == "." || s == ".." {
		return false
	}

	return !strings.ContainsFunc(s, func(r rune) bool {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'

		return !alnum && r != '.' && r != '-' && r != '_'
	})
}

func (m *Manager) now() time.Time {
	if m.Now == nil {
		return time.Now().UTC()
	}

	return m.Now().UTC()
}
