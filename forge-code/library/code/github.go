package code

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// githubBackend maps the GitHub REST API, on github.com and GitHub
// Enterprise Server, to the backend seam. It reads Git data through the
// git/* endpoints by object ID, never through contents, which follows
// symlinks and falls back to the default branch.
type githubBackend struct{}

// githubMaxBlob is the largest blob GitHub's blob API serves.
const githubMaxBlob = 100 << 20

// githubMaxPeel caps how many annotated tags one ref may nest.
const githubMaxPeel = 4

// githubMaxRefsBody caps the unpaged git/matching-refs response.
const githubMaxRefsBody = 32 << 20

// githubRawBlob is the media type for raw blob content.
const githubRawBlob = "application/vnd.github.raw+json"

func (githubBackend) capabilities() Capabilities {
	return Capabilities{TreeIDs: true, MergeBase: true}
}

// githubRepo returns the API path of o's repository.
func githubRepo(o *op) (string, error) {
	p, ok := reposPath(o.repo)
	if !ok {
		return "", fmt.Errorf("%w: GitHub repository %q, want owner/name", ErrInvalid, o.repo)
	}

	return p, nil
}

// githubEmpty reports a 409, which GitHub sends for Git data in an empty
// repository ("Git Repository is empty.").
func githubEmpty(err error) bool {
	var fe *forgehttp.Error
	return errors.As(err, &fe) && fe.StatusCode == http.StatusConflict
}

// githubTooLarge reports GitHub's refusal to serve a large blob.
func githubTooLarge(err error) bool {
	var fe *forgehttp.Error
	return errors.As(err, &fe) && (fe.StatusCode == http.StatusForbidden ||
		fe.StatusCode == http.StatusUnprocessableEntity) &&
		strings.Contains(strings.ToLower(fe.Message), "too large")
}

func (githubBackend) defaultBranch(o *op) (string, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return "", err
	}

	var v struct {
		DefaultBranch string `json:"default_branch"`
	}
	err = forgerest.Send(o.ctx, o.s, http.MethodGet, repo, nil, &v)

	return v.DefaultBranch, err
}

// githubSHA is an object reference in GitHub JSON.
type githubSHA struct {
	SHA string `json:"sha"`
}

// githubPerson is an author or committer in GitHub JSON.
type githubPerson struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

func (p githubPerson) shared() Signature {
	return Signature{Name: p.Name, Email: p.Email, When: p.Date}
}

// githubGitCommit is a git/commits object, and the "commit" field of a
// /commits list item.
type githubGitCommit struct {
	SHA       string       `json:"sha"`
	Tree      githubSHA    `json:"tree"`
	Parents   []githubSHA  `json:"parents"`
	Author    githubPerson `json:"author"`
	Committer githubPerson `json:"committer"`
	Message   string       `json:"message"`
}

// githubListCommit is a /commits or compare list item.
type githubListCommit struct {
	SHA     string          `json:"sha"`
	Commit  githubGitCommit `json:"commit"`
	Parents []githubSHA     `json:"parents"`
}

// githubCommit converts a commit with its ID and parents.
func githubCommit(sha string, c githubGitCommit, parents []githubSHA) (Commit, error) {
	id, err := parseOID(sha)
	if err != nil {
		return Commit{}, err
	}

	tree, err := parseOID(c.Tree.SHA)
	if err != nil {
		return Commit{}, err
	}

	out := Commit{
		ID:        id,
		Tree:      tree,
		Parents:   []OID{},
		Author:    c.Author.shared(),
		Committer: c.Committer.shared(),
		Message:   commitMessage(c.Message),
	}
	for _, p := range parents {
		pid, err := parseOID(p.SHA)
		if err != nil {
			return Commit{}, err
		}

		out.Parents = append(out.Parents, pid)
	}

	return out, nil
}

func (githubBackend) commit(o *op, id OID) (Commit, error) {
	return cached(o, "github:commit:"+string(id), func() (Commit, error) {
		repo, err := githubRepo(o)
		if err != nil {
			return Commit{}, err
		}

		var v githubGitCommit
		err = forgerest.Send(o.ctx, o.s, http.MethodGet, repo+"/git/commits/"+string(id), nil, &v)
		if err != nil {
			if githubEmpty(err) {
				return Commit{}, objectMissing(id)
			}

			return Commit{}, missing(err, objectMissing(id))
		}

		c, err := githubCommit(v.SHA, v, v.Parents)
		if err == nil && c.ID != id {
			err = fmt.Errorf("code: GitHub sent commit %s for %s", c.ID, id)
		}

		return c, err
	})
}

func (githubBackend) history(o *op, tip OID, path string, n int, pos historyPos) ([]Commit, historyPos, bool, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return nil, pos, false, err
	}

	q := url.Values{
		"sha":        {string(tip)},
		paramPerPage: {strconv.Itoa(n)},
		"page":       {strconv.Itoa(pos.Page + 1)},
	}
	if path != "" {
		q.Set("path", path)
	}

	var items []githubListCommit
	h, err := forgerest.Do(o.ctx, o.s, http.MethodGet, repo+"/commits?"+q.Encode(), nil, &items)
	if err != nil {
		// GitHub answers 404 for an unknown sha (422 on older versions), and
		// 409 in an empty repository. A 404 may also mean the repository is
		// gone, so check it before blaming the tip.
		if isMissing(err) {
			if _, rerr := (githubBackend{}).defaultBranch(o); rerr != nil {
				return nil, pos, false, rerr
			}
		}
		if githubEmpty(err) || isMissing(err) {
			return nil, pos, false, objectMissing(tip)
		}

		return nil, pos, false, err
	}

	out := make([]Commit, 0, len(items))
	for _, it := range items {
		c, err := githubCommit(it.SHA, it.Commit, it.Parents)
		if err != nil {
			return nil, pos, false, err
		}

		out = append(out, c)
	}

	link, ok := forgehttp.NextPage(h)

	return out, historyPos{Page: pos.Page + 1}, ok && link != "" && len(items) > 0, nil
}

func (githubBackend) mergeBase(o *op, a, b OID) (OID, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return "", err
	}

	var v struct {
		MergeBase githubSHA `json:"merge_base_commit"`
	}
	path := repo + "/compare/" + string(a) + "..." + string(b) + "?per_page=1"
	if err := forgerest.Send(o.ctx, o.s, http.MethodGet, path, nil, &v); err != nil {
		// Both commits exist, so a 404 means they share no history.
		if isMissing(err) {
			return "", errNoMergeBase
		}

		return "", err
	}

	return parseOID(v.MergeBase.SHA)
}
