package sources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

// hookEvents are the native events forge-reactor turns into events.
func hookEvents() []string { return []string{"push", "pull_request"} }

// hookBody is a create or edit request for one webhook. GitHub and Forgejo
// share this shape, apart from the name or type.
type hookBody struct {
	Name   string            `json:"name,omitempty"`
	Type   string            `json:"type,omitempty"`
	Active bool              `json:"active"`
	Events []string          `json:"events"`
	Config map[string]string `json:"config"`
}

// body returns the request for src's hook.
func (m *Manager) body(src Source, create bool) hookBody {
	b := hookBody{
		Active: true,
		Events: hookEvents(),
		Config: map[string]string{"url": m.URL(src.ID), "content_type": "json", "secret": src.Secret},
	}
	if src.Forge == forgeauth.GitHub {
		b.Config["insecure_ssl"] = "0"
	}

	switch {
	case !create:
	case src.Forge == forgeauth.GitHub:
		b.Name = "web"
	default:
		b.Type = "forgejo"
	}

	return b
}

// session opens a session for src and checks its forge.
func (m *Manager) session(ctx context.Context, src Source) (*forgeauth.Session, error) {
	if !m.CanRegister() {
		return nil, ErrNotConfigured
	}
	s, err := m.Sessions(ctx, src.Connection)
	switch {
	case errors.Is(err, reactor.ErrNotFound):
		return nil, fmt.Errorf("%w: connection", reactor.ErrNotFound)
	case err != nil:
		return nil, ErrRegistration
	case s == nil || s.Kind != src.Forge:
		return nil, fmt.Errorf("%w: the connection is not a %s connection", reactor.ErrInvalid, src.Forge)
	}

	return s, nil
}

// register creates src's hook on every repository. On failure it returns
// the hooks created so far with the error.
func (m *Manager) register(ctx context.Context, src Source) (map[string]int64, error) {
	s, err := m.session(ctx, src)
	if err != nil {
		return nil, err
	}

	hooks := map[string]int64{}
	for _, repo := range src.Repositories {
		var created struct {
			ID int64 `json:"id"`
		}
		err := forgerest.Send(ctx, s, http.MethodPost, hooksPath(repo), m.body(src, true), &created)
		if err != nil || created.ID <= 0 {
			return hooks, ErrRegistration
		}
		hooks[repo] = created.ID
	}

	return hooks, nil
}

// update sends src's secret to every registered hook and returns the
// repositories that failed.
func (m *Manager) update(ctx context.Context, src Source) []string {
	return m.eachHook(ctx, src, func(s *forgeauth.Session, path string) error {
		return forgerest.Send(ctx, s, http.MethodPatch, path, m.body(src, false), nil)
	})
}

// unregister deletes every registered hook of src and returns the
// repositories that failed. A hook already gone counts as deleted.
func (m *Manager) unregister(ctx context.Context, src Source) []string {
	return m.eachHook(ctx, src, func(s *forgeauth.Session, path string) error {
		err := forgerest.Send(ctx, s, http.MethodDelete, path, nil, nil)
		if errors.Is(err, forgehttp.ErrNotFound) {
			return nil
		}

		return err
	})
}

// eachHook runs fn for every registered hook and returns the sorted
// repositories where it failed.
func (m *Manager) eachHook(ctx context.Context, src Source, fn func(*forgeauth.Session, string) error) []string {
	if !src.Registered() {
		return nil
	}

	var failed []string
	s, err := m.session(ctx, src)
	for repo, id := range src.Hooks {
		if err != nil || fn(s, hooksPath(repo)+"/"+strconv.FormatInt(id, 10)) != nil {
			failed = append(failed, repo)
		}
	}
	slices.Sort(failed)

	return failed
}

// hooksPath is the hooks collection of an owner/name repository.
func hooksPath(repo string) string {
	owner, name, _ := strings.Cut(repo, "/")

	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/hooks"
}
