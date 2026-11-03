package code

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// forgejoRef is one entry of git/refs.
type forgejoRef struct {
	Ref    string `json:"ref"`
	Object struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"object"`
}

// decode returns the ref with Commit set if it points to a commit.
func (r forgejoRef) decode() (Ref, error) {
	target, err := parseOID(r.Object.SHA)
	if err != nil {
		return Ref{}, err
	}

	ref := Ref{Name: r.Ref, Target: target}
	if r.Object.Type == "commit" {
		ref.Commit = target
	}

	return ref, nil
}

// forgejoRefs reads git/refs/{filter}. The forge matches refs whose names
// start with "refs/"+filter and answers 404 if none do. It sends an array,
// or in some versions one object for an exact match. The listing is not
// paged, so its size is capped.
func forgejoRefs(o *op, filter string) ([]forgejoRef, error) {
	repo, err := forgejoRepo(o)
	if err != nil {
		return nil, err
	}

	resp, err := forgerest.Open(o.ctx, o.s, http.MethodGet, repo+"/git/refs/"+escapePath(filter),
		acceptJSON())
	if errors.Is(err, forgehttp.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, forgejoMaxRefsBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > forgejoMaxRefsBody {
		return nil, fmt.Errorf("%w: ref listing over %d bytes", ErrTooLarge, forgejoMaxRefsBody)
	}

	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '{' {
		var one forgejoRef
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, fmt.Errorf("code: decode refs: %w", err)
		}

		return []forgejoRef{one}, nil
	}

	var many []forgejoRef
	if err := json.Unmarshal(data, &many); err != nil {
		return nil, fmt.Errorf("code: decode refs: %w", err)
	}

	return many, nil
}

// forgejoPeel returns the commit the annotated tag id points to, through
// any chain of tags. git/commits peels tag objects itself, while git/tags
// only answers for a tag that a ref points to directly, so a nested tag
// could not be followed with it. It returns "" if the chain ends in a tree
// or blob or the tag is missing.
func forgejoPeel(o *op, id OID) (OID, error) {
	return cached(o, "peel:"+string(id), func() (OID, error) {
		var v forgejoCommit
		if _, err := forgejoGet(o, "/git/commits/"+string(id)+"?"+forgejoLean, &v); err != nil {
			if isMissing(err) {
				return "", nil
			}

			return "", err
		}

		c, err := v.decode()
		if err != nil {
			return "", err
		}
		if c.ID == id {
			return "", fmt.Errorf("code: forge sent commit %s for tag %s", c.ID, id)
		}

		// Keep the commit for a later read in the same call.
		_, _ = cached(o, "commit:"+string(c.ID), func() (Commit, error) { return c, nil })

		return c.ID, nil
	})
}

func (forgejoBackend) ref(o *op, name string) (Ref, bool, error) {
	list, err := forgejoRefs(o, strings.TrimPrefix(name, "refs/"))
	if err != nil {
		return Ref{}, false, err
	}

	for _, fr := range list {
		if fr.Ref != name {
			continue // the forge matched a longer name
		}

		ref, err := fr.decode()
		if err != nil {
			return Ref{}, false, err
		}
		if fr.Object.Type == "tag" {
			if ref.Commit, err = forgejoPeel(o, ref.Target); err != nil {
				return Ref{}, false, err
			}
		}

		return ref, true, nil
	}

	return Ref{}, false, nil
}

func (forgejoBackend) refs(o *op, prefix string, limit int) ([]Ref, bool, error) {
	list, err := forgejoRefs(o, strings.TrimPrefix(prefix, "refs/"))
	if err != nil {
		return nil, false, err
	}

	slices.SortFunc(list, func(a, b forgejoRef) int { return strings.Compare(a.Ref, b.Ref) })
	var refs []Ref
	var tagged []int // indexes of annotated tags in refs
	tagObjs := map[OID]bool{}
	for _, fr := range list {
		if fr.Object.Type == "tag" {
			if id, err := parseOID(fr.Object.SHA); err == nil {
				tagObjs[id] = true
			}
		}
	}

	truncated := false
	for _, fr := range list {
		if !strings.HasPrefix(fr.Ref, prefix) {
			continue
		}
		if len(refs) == limit {
			truncated = true
			break
		}

		ref, err := fr.decode()
		if err != nil {
			return nil, false, err
		}
		if fr.Object.Type == "tag" {
			tagged = append(tagged, len(refs))
		}

		refs = append(refs, ref)
	}

	if len(tagged) == 0 {
		return refs, truncated, nil
	}

	done, err := forgejoPeelTags(o, refs, tagged, tagObjs)
	if err != nil {
		return nil, false, err
	}
	if done < len(refs) {
		return refs[:done], true, nil
	}

	return refs, truncated, nil
}

// forgejoPeelTags sets Commit on the annotated tags refs[i] for i in
// tagged, in order. It joins them by name with the paged /tags listing,
// whose commit.sha is the tag's target, peeled one level; a target that is
// itself a listed tag is followed. A target still known to be a tag, from
// tagObjs (every annotated tag object in git/refs) or a long chain, and
// tags missing from /tags, such as ones added since git/refs, are peeled
// with forgejoPeel. If the call budget runs out it returns how many
// leading refs are complete.
//
// /tags cannot tell a commit from an unlisted tag object, so a tag whose
// target is an annotated tag without a ref of its own reports that tag
// object as its commit.
func forgejoPeelTags(o *op, refs []Ref, tagged []int, tagObjs map[OID]bool) (int, error) {
	l, err := forgejoListTags(o, refs, tagged)
	if err != nil {
		return 0, err
	}

	for _, i := range tagged {
		target, err := l.peel(o, refs[i].Target, i, tagObjs)
		if exhausted(err) {
			return i, nil
		}
		if err != nil {
			return 0, err
		}

		refs[i].Commit = target
	}

	return len(refs), nil
}

// forgejoTagList is what the /tags listing told about annotated tags.
type forgejoTagList struct {
	// targets maps an annotated tag object to its target.
	targets map[OID]OID
	// found maps an index into refs to the target of its tag.
	found map[int]OID
}

// forgejoListTags walks /tags until it has found every tagged ref, or the
// call budget runs out.
func forgejoListTags(o *op, refs []Ref, tagged []int) (*forgejoTagList, error) {
	want := map[string]int{}
	for _, i := range tagged {
		want[strings.TrimPrefix(refs[i].Name, tagsPrefix)] = i
	}

	repo, err := forgejoRepo(o)
	if err != nil {
		return nil, err
	}

	l := &forgejoTagList{targets: map[OID]OID{}, found: map[int]OID{}}
	q := url.Values{"limit": {strconv.Itoa(forgejoTagPage)}}
	err = forgerest.Each(o.ctx, o.s, repo+"/tags?"+q.Encode(), func(raw json.RawMessage) (bool, error) {
		name, id, target, err := forgejoDecodeTag(raw)
		if err != nil {
			return false, err
		}
		if id != target {
			l.targets[id] = target
		}
		if i, ok := want[name]; ok && refs[i].Target == id {
			l.found[i] = target
		}

		return len(l.found) < len(tagged), nil
	})
	if err != nil && !exhausted(err) {
		return nil, err
	}

	return l, nil
}

// forgejoDecodeTag decodes a /tags entry: its name, its object ID and
// its target, peeled one level.
func forgejoDecodeTag(raw json.RawMessage) (name string, id, target OID, err error) {
	var v struct {
		Name   string     `json:"name"`
		ID     string     `json:"id"`
		Commit forgejoSHA `json:"commit"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", "", "", err
	}

	if id, err = parseOID(v.ID); err != nil {
		return "", "", "", err
	}
	if target, err = parseOID(v.Commit.SHA); err != nil {
		return "", "", "", err
	}

	return v.Name, id, target, nil
}

// peel returns the commit of refs[i], whose tag object is tag. It follows
// listed tags, and peels with forgejoPeel what is still a tag: missing
// from /tags, a long chain, or an inner tag whose /tags entry came after
// the walk stopped.
func (l *forgejoTagList) peel(o *op, tag OID, i int, tagObjs map[OID]bool) (OID, error) {
	target, ok := l.found[i]
	if !ok {
		return forgejoPeel(o, tag)
	}

	for n := 1; n < forgejoMaxPeel && l.targets[target] != ""; n++ {
		target = l.targets[target]
	}

	if l.targets[target] != "" || tagObjs[target] {
		return forgejoPeel(o, target)
	}

	return target, nil
}
