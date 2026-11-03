package code

import (
	"fmt"
	"net/http"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// forgejoBackend maps Forgejo's REST API (v1) to the backend seam. Every
// path is below /repos/{owner}/{name}.
type forgejoBackend struct{}

// Forgejo page sizes and caps.
const (
	// forgejoCommitPage is the /commits page size. It must equal the
	// forge's [git] COMMITS_RANGE_SIZE (50), since with a path filter the
	// forge skips (page-1)*COMMITS_RANGE_SIZE commits whatever the limit.
	// forgejoLog checks the forge's page headers and fails with
	// ErrUnsupported rather than skip commits.
	forgejoCommitPage = 50
	// forgejoTreePage is the git/trees page size, the forge's maximum.
	forgejoTreePage = 1000
	// forgejoTagPage is the /tags page size, the forge's maximum.
	forgejoTagPage = 50
	// forgejoMaxRefsBody caps the unpaged git/refs response.
	forgejoMaxRefsBody = 32 << 20
	// forgejoMaxPeel is how many tag objects ListRefs follows through the
	// /tags listing before it peels the rest with git/commits.
	forgejoMaxPeel = 4
	// forgejoMaxBases caps merge base candidates.
	forgejoMaxBases = 8
)

// forgejoLean turns off the slow parts of Forgejo commit responses.
const forgejoLean = "stat=false&verification=false&files=false"

func (forgejoBackend) capabilities() Capabilities {
	return Capabilities{TreeIDs: true, MergeBase: true}
}

// forgejoRepo returns the API path of o's repository.
func forgejoRepo(o *op) (string, error) {
	p, ok := reposPath(o.repo)
	if !ok {
		return "", fmt.Errorf("%w: repository %q, want owner/name on Forgejo", ErrInvalid, o.repo)
	}

	return p, nil
}

// forgejoGet decodes the JSON response of GET target below the repository.
func forgejoGet(o *op, target string, v any) (http.Header, error) {
	repo, err := forgejoRepo(o)
	if err != nil {
		return nil, err
	}

	return forgerest.Do(o.ctx, o.s, http.MethodGet, repo+target, nil, v)
}

func (forgejoBackend) defaultBranch(o *op) (string, error) {
	var v struct {
		DefaultBranch string `json:"default_branch"`
		Empty         bool   `json:"empty"`
	}
	if _, err := forgejoGet(o, "", &v); err != nil {
		return "", err
	}
	if v.Empty {
		return "", nil
	}

	return v.DefaultBranch, nil
}

// forgejoSig is a commit author or committer.
type forgejoSig struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

func (s forgejoSig) decode() (Signature, error) {
	t, err := time.Parse(time.RFC3339, s.Date)
	if err != nil {
		return Signature{}, fmt.Errorf("code: forge sent invalid date %q", s.Date)
	}

	return Signature{Name: s.Name, Email: s.Email, When: t}, nil
}

// forgejoSHA is an object reference such as a parent or a tree.
type forgejoSHA struct {
	SHA string `json:"sha"`
}

// forgejoCommit is a commit from git/commits or /commits.
type forgejoCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Author    forgejoSig `json:"author"`
		Committer forgejoSig `json:"committer"`
		Message   string     `json:"message"`
		Tree      forgejoSHA `json:"tree"`
	} `json:"commit"`
	Parents []forgejoSHA `json:"parents"`
}

func (c forgejoCommit) decode() (Commit, error) {
	out := Commit{Parents: make([]OID, 0, len(c.Parents))}
	var err error
	if out.ID, err = parseOID(c.SHA); err != nil {
		return Commit{}, err
	}
	if out.Tree, err = parseOID(c.Commit.Tree.SHA); err != nil {
		return Commit{}, err
	}

	for _, p := range c.Parents {
		id, err := parseOID(p.SHA)
		if err != nil {
			return Commit{}, err
		}

		out.Parents = append(out.Parents, id)
	}

	if out.Author, err = c.Commit.Author.decode(); err != nil {
		return Commit{}, err
	}
	if out.Committer, err = c.Commit.Committer.decode(); err != nil {
		return Commit{}, err
	}

	out.Message = commitMessage(c.Commit.Message)

	return out, nil
}

func (forgejoBackend) commit(o *op, id OID) (Commit, error) {
	return cached(o, "commit:"+string(id), func() (Commit, error) {
		var v forgejoCommit
		if _, err := forgejoGet(o, "/git/commits/"+string(id)+"?"+forgejoLean, &v); err != nil {
			return Commit{}, missing(err, objectMissing(id))
		}

		c, err := v.decode()
		if err != nil {
			return Commit{}, err
		}
		if c.ID != id {
			// The forge peels a tag ID to its commit.
			return Commit{}, objectMissing(id)
		}

		return c, nil
	})
}
