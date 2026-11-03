package code

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// githubObject is the target of a ref or tag in GitHub JSON.
type githubObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// githubRefJSON is a git/ref or git/matching-refs item.
type githubRefJSON struct {
	Ref    string       `json:"ref"`
	Object githubObject `json:"object"`
}

func (githubBackend) ref(o *op, name string) (Ref, bool, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return Ref{}, false, err
	}

	var v githubRefJSON
	path := repo + "/git/ref/" + escapePath(strings.TrimPrefix(name, "refs/"))
	err = forgerest.Send(o.ctx, o.s, http.MethodGet, path, nil, &v)
	switch {
	case isMissing(err) || githubEmpty(err):
		return Ref{}, false, nil
	case err != nil:
		return Ref{}, false, err
	case v.Ref != name:
		return Ref{}, false, nil
	}

	ref, err := githubPeel(o, repo, name, v.Object)

	return ref, err == nil, err
}

// githubPeel returns the ref name pointing at obj, peeling annotated tags
// through git/tags. A dangling tag leaves Commit empty; a chain deeper
// than githubMaxPeel is ErrUnsupported.
func githubPeel(o *op, repo, name string, obj githubObject) (Ref, error) {
	target, err := parseOID(obj.SHA)
	if err != nil {
		return Ref{}, err
	}

	ref := Ref{Name: name, Target: target}
	typ, id := obj.Type, target
	for depth := 0; typ == string(ObjectTag); depth++ {
		if depth == githubMaxPeel {
			return Ref{}, fmt.Errorf("%w: tag %s nests more than %d tags", ErrUnsupported, name, githubMaxPeel)
		}

		var t struct {
			Object githubObject `json:"object"`
		}
		err := forgerest.Send(o.ctx, o.s, http.MethodGet, repo+"/git/tags/"+string(id), nil, &t)
		if isMissing(err) {
			return ref, nil
		}
		if err != nil {
			return Ref{}, err
		}

		typ = t.Object.Type
		if id, err = parseOID(t.Object.SHA); err != nil {
			return Ref{}, err
		}
	}

	if typ == string(ObjectCommit) {
		ref.Commit = id
	}

	return ref, nil
}

// githubMatching reads git/matching-refs for prefix, keeping only names
// that start with it. GitHub does not page this listing and ignores
// per_page, so the whole response is read, capped at githubMaxRefsBody.
func githubMatching(o *op, repo, prefix string) ([]githubRefJSON, error) {
	resp, err := forgerest.Open(o.ctx, o.s, http.MethodGet,
		repo+"/git/matching-refs/"+escapePath(strings.TrimPrefix(prefix, "refs/")),
		acceptJSON())
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, githubMaxRefsBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > githubMaxRefsBody {
		return nil, fmt.Errorf("%w: ref listing over %d bytes", ErrTooLarge, githubMaxRefsBody)
	}

	var all []githubRefJSON
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("code: decode refs: %w", err)
	}

	return slices.DeleteFunc(all, func(v githubRefJSON) bool { return !strings.HasPrefix(v.Ref, prefix) }), nil
}

func (githubBackend) refs(o *op, prefix string, limit int) ([]Ref, bool, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return nil, false, err
	}

	listed, err := githubMatching(o, repo, prefix)
	switch {
	case githubEmpty(err):
		return nil, false, nil
	case exhausted(err):
		return nil, true, nil
	case err != nil:
		return nil, false, err
	}

	slices.SortFunc(listed, func(a, b githubRefJSON) int { return strings.Compare(a.Ref, b.Ref) })
	truncated := false
	if len(listed) > limit {
		listed, truncated = listed[:limit], true
	}

	state, err := newGitHubRefs(listed)
	if err != nil {
		return nil, false, err
	}

	joined, err := state.joinTags(o, repo)
	if err != nil {
		return nil, false, err
	}
	if err := state.checkMovedTags(o, repo, prefix, joined); err != nil {
		return nil, false, err
	}
	if err := state.peelRemaining(o, repo); err != nil {
		return nil, false, err
	}

	// Keep complete refs before the first one left incomplete by the call budget.
	if i := slices.Index(state.done, false); i >= 0 {
		return state.refs[:i], true, nil
	}

	return state.refs, truncated, nil
}

type githubRefs struct {
	listed    []githubRefJSON
	refs      []Ref
	done      []bool
	annotated map[string]int
}

func newGitHubRefs(listed []githubRefJSON) (*githubRefs, error) {
	state := &githubRefs{
		listed:    listed,
		refs:      make([]Ref, len(listed)),
		done:      make([]bool, len(listed)),
		annotated: map[string]int{},
	}
	for i, value := range listed {
		target, err := parseOID(value.Object.SHA)
		if err != nil {
			return nil, err
		}

		state.refs[i] = Ref{Name: value.Ref, Target: target}
		switch value.Object.Type {
		case string(ObjectTag):
			state.annotated[strings.TrimPrefix(value.Ref, tagsPrefix)] = i
			continue
		case string(ObjectCommit):
			state.refs[i].Commit = target
		}

		state.done[i] = true
	}

	return state, nil
}

// Join annotated tags with the peeled commits from /tags without a call per tag.
func (s *githubRefs) joinTags(o *op, repo string) ([]int, error) {
	var joined []int
	left := len(s.annotated)
	if left == 0 {
		return joined, nil
	}

	err := forgerest.Each(o.ctx, o.s, repo+"/tags?per_page=100", func(raw json.RawMessage) (bool, error) {
		var tag struct {
			Name   string    `json:"name"`
			Commit githubSHA `json:"commit"`
		}
		if err := json.Unmarshal(raw, &tag); err != nil {
			return false, err
		}

		i, ok := s.annotated[tag.Name]
		if !ok || s.done[i] {
			return true, nil
		}

		commit, err := parseOID(tag.Commit.SHA)
		if err != nil {
			return false, err
		}

		s.refs[i].Commit = commit
		s.done[i] = true
		joined = append(joined, i)
		left--

		return left > 0, nil
	})
	if err != nil && !exhausted(err) {
		return nil, err
	}

	return joined, nil
}

// /tags has no tag object IDs. Recheck objects to detect moves during the join.
func (s *githubRefs) checkMovedTags(o *op, repo, prefix string, joined []int) error {
	if len(joined) == 0 {
		return nil
	}

	again, err := githubMatching(o, repo, prefix)
	if err != nil && !exhausted(err) && !githubEmpty(err) {
		return err
	}

	now := map[string]githubObject{}
	for _, value := range again {
		now[value.Ref] = value.Object
	}

	for _, i := range joined {
		object, ok := now[s.refs[i].Name]
		if ok && strings.EqualFold(object.SHA, string(s.refs[i].Target)) {
			continue
		}

		s.done[i] = false
		if ok {
			s.listed[i].Object = object
		}
	}

	return nil
}

// Remaining tags may point to non-commits, have moved, or have disappeared.
func (s *githubRefs) peelRemaining(o *op, repo string) error {
	for i := range s.refs {
		if s.done[i] {
			continue
		}

		object := githubObject{Type: s.listed[i].Object.Type, SHA: s.listed[i].Object.SHA}
		ref, err := githubPeel(o, repo, s.refs[i].Name, object)
		switch {
		case err == nil:
			s.refs[i] = ref
			s.done[i] = true
		case errors.Is(err, ErrUnsupported):
			s.done[i] = true
		case exhausted(err):
		default:
			return err
		}
	}

	return nil
}
