package repositories

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// forge maps one forge's REST API to the shared model. Adapters are
// internal; callers only see the shared types.
type forge interface {
	// listPath is the first page of the account's repositories.
	listPath() string
	// repoPath is the API path of a repository, or false if the forge
	// cannot hold such a path.
	repoPath(path string) (string, bool)
	// branchesPath and tagsPath are the first pages of a repository's refs.
	branchesPath(repo string) string
	tagsPath(repo string) string
	// updateMethod is the HTTP method that edits a repository.
	updateMethod() string
	// repository, branch and tag decode one API object. Repository.Path
	// must be set; Owner, Name and Source are filled in by the caller.
	repository(raw rawJSON) (Repository, error)
	branch(raw rawJSON) (Branch, error)
	tag(raw rawJSON) (Tag, error)
}

// forges holds the supported adapters by kind.
var forges = map[forgeauth.Kind]forge{
	forgeauth.GitHub:  github{},
	forgeauth.GitLab:  gitlab{},
	forgeauth.Forgejo: forgejo{},
}

// ownerName escapes a two-segment path as "owner/name" for GitHub and
// Forgejo URLs.
func ownerName(path string) (string, bool) {
	owner, name, ok := strings.Cut(path, "/")
	if !ok || strings.Contains(name, "/") {
		return "", false
	}
	return url.PathEscape(owner) + "/" + url.PathEscape(name), true
}

// id formats a numeric forge ID.
func id(n int64) string { return strconv.FormatInt(n, 10) }

// github is the GitHub REST API.
type github struct{}

type githubRepo struct {
	ID            int64     `json:"id"`
	FullName      string    `json:"full_name"`
	Description   string    `json:"description"`
	DefaultBranch string    `json:"default_branch"`
	Private       bool      `json:"private"`
	Visibility    string    `json:"visibility"`
	Archived      bool      `json:"archived"`
	Fork          bool      `json:"fork"`
	HTMLURL       string    `json:"html_url"`
	CloneURL      string    `json:"clone_url"`
	SSHURL        string    `json:"ssh_url"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (github) listPath() string                    { return "/user/repos?per_page=100" }
func (github) repoPath(path string) (string, bool) { return prefixed("/repos/", path) }
func (github) branchesPath(repo string) string     { return repo + "/branches?per_page=100" }
func (github) tagsPath(repo string) string         { return repo + "/tags?per_page=100" }
func (github) updateMethod() string                { return http.MethodPatch }

func (github) repository(raw rawJSON) (Repository, error) {
	var v githubRepo
	if err := json.Unmarshal(raw, &v); err != nil {
		return Repository{}, err
	}
	vis := Visibility(v.Visibility)
	if vis == "" {
		vis = visibility(v.Private, false)
	}
	return Repository{
		ID: id(v.ID), Path: v.FullName, Description: v.Description,
		DefaultBranch: v.DefaultBranch, Visibility: vis, Archived: v.Archived,
		Fork: v.Fork, WebURL: v.HTMLURL, CloneURL: v.CloneURL, SSHURL: v.SSHURL,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}, nil
}

func (github) branch(raw rawJSON) (Branch, error) {
	var v struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
		Protected bool `json:"protected"`
	}
	err := json.Unmarshal(raw, &v)
	return Branch{Name: v.Name, SHA: v.Commit.SHA, Protected: v.Protected}, err
}

func (github) tag(raw rawJSON) (Tag, error) {
	var v struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	err := json.Unmarshal(raw, &v)
	return Tag{Name: v.Name, SHA: v.Commit.SHA}, err
}

// gitlab is the GitLab REST API (v4).
type gitlab struct{}

type gitlabProject struct {
	ID                int64           `json:"id"`
	PathWithNamespace string          `json:"path_with_namespace"`
	Description       string          `json:"description"`
	DefaultBranch     string          `json:"default_branch"`
	Visibility        string          `json:"visibility"`
	Archived          bool            `json:"archived"`
	ForkedFrom        json.RawMessage `json:"forked_from_project"`
	WebURL            string          `json:"web_url"`
	HTTPURL           string          `json:"http_url_to_repo"`
	SSHURL            string          `json:"ssh_url_to_repo"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	LastActivityAt    time.Time       `json:"last_activity_at"`
}

func (gitlab) listPath() string { return "/projects?membership=true&per_page=100" }

// repoPath encodes the full path as one segment, such as
// /projects/group%2Fsub%2Fproject.
func (gitlab) repoPath(path string) (string, bool) {
	return "/projects/" + url.PathEscape(path), true
}

func (gitlab) branchesPath(repo string) string { return repo + "/repository/branches?per_page=100" }
func (gitlab) tagsPath(repo string) string     { return repo + "/repository/tags?per_page=100" }
func (gitlab) updateMethod() string            { return http.MethodPut }

func (gitlab) repository(raw rawJSON) (Repository, error) {
	var v gitlabProject
	if err := json.Unmarshal(raw, &v); err != nil {
		return Repository{}, err
	}
	updated := v.UpdatedAt
	if updated.IsZero() {
		updated = v.LastActivityAt
	}
	fork := len(v.ForkedFrom) > 0 && string(v.ForkedFrom) != "null"
	return Repository{
		ID: id(v.ID), Path: v.PathWithNamespace, Description: v.Description,
		DefaultBranch: v.DefaultBranch, Visibility: Visibility(v.Visibility),
		Archived: v.Archived, Fork: fork, WebURL: v.WebURL, CloneURL: v.HTTPURL,
		SSHURL: v.SSHURL, CreatedAt: v.CreatedAt, UpdatedAt: updated,
	}, nil
}

func (gitlab) branch(raw rawJSON) (Branch, error) {
	var v struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
		Protected bool `json:"protected"`
	}
	err := json.Unmarshal(raw, &v)
	return Branch{Name: v.Name, SHA: v.Commit.ID, Protected: v.Protected}, err
}

func (gitlab) tag(raw rawJSON) (Tag, error) {
	var v struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	err := json.Unmarshal(raw, &v)
	return Tag{Name: v.Name, SHA: v.Commit.ID}, err
}

// forgejo is the Forgejo REST API (v1). Its page size parameter is limit.
type forgejo struct{}

type forgejoRepo struct {
	ID            int64     `json:"id"`
	FullName      string    `json:"full_name"`
	Description   string    `json:"description"`
	DefaultBranch string    `json:"default_branch"`
	Private       bool      `json:"private"`
	Internal      bool      `json:"internal"`
	Archived      bool      `json:"archived"`
	Fork          bool      `json:"fork"`
	HTMLURL       string    `json:"html_url"`
	CloneURL      string    `json:"clone_url"`
	SSHURL        string    `json:"ssh_url"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (forgejo) listPath() string                    { return "/user/repos?limit=50" }
func (forgejo) repoPath(path string) (string, bool) { return prefixed("/repos/", path) }
func (forgejo) branchesPath(repo string) string     { return repo + "/branches?limit=50" }
func (forgejo) tagsPath(repo string) string         { return repo + "/tags?limit=50" }
func (forgejo) updateMethod() string                { return http.MethodPatch }

func (forgejo) repository(raw rawJSON) (Repository, error) {
	var v forgejoRepo
	if err := json.Unmarshal(raw, &v); err != nil {
		return Repository{}, err
	}
	return Repository{
		ID: id(v.ID), Path: v.FullName, Description: v.Description,
		DefaultBranch: v.DefaultBranch, Visibility: visibility(v.Private, v.Internal),
		Archived: v.Archived, Fork: v.Fork, WebURL: v.HTMLURL, CloneURL: v.CloneURL,
		SSHURL: v.SSHURL, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}, nil
}

func (forgejo) branch(raw rawJSON) (Branch, error) {
	var v struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
		Protected bool `json:"protected"`
	}
	err := json.Unmarshal(raw, &v)
	return Branch{Name: v.Name, SHA: v.Commit.ID, Protected: v.Protected}, err
}

func (forgejo) tag(raw rawJSON) (Tag, error) {
	var v struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	err := json.Unmarshal(raw, &v)
	return Tag{Name: v.Name, SHA: v.Commit.SHA}, err
}

// prefixed returns prefix plus the escaped "owner/name" path.
func prefixed(prefix, path string) (string, bool) {
	p, ok := ownerName(path)
	if !ok {
		return "", false
	}
	return prefix + p, true
}

// visibility maps private and internal flags to a Visibility.
func visibility(private, internal bool) Visibility {
	switch {
	case private:
		return Private
	case internal:
		return Internal
	}
	return Public
}
