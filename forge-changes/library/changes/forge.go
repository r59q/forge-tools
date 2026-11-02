package changes

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

type wireUser struct {
	Login    string
	Username string
	HTMLURL  string `json:"html_url"`
	WebURL   string `json:"web_url"`
}

func (u wireUser) user() User {
	if u.Username != "" {
		return User{u.Username, u.WebURL}
	}
	return User{u.Login, u.HTMLURL}
}

type wireBranch struct {
	Ref  string
	SHA  string
	Repo *struct {
		FullName string `json:"full_name"`
	}
}
type proposal struct {
	change      ChangeRequest
	nodeID      string
	mergeable   *bool
	mergeStatus string
	files       *int
}

func decode(s Source, path string, raw json.RawMessage) (proposal, error) {
	var w struct {
		ID             int64
		Number         int64
		IID            int64
		NodeID         string `json:"node_id"`
		Title          string
		Body           string
		Description    string
		State          string
		Draft          bool
		WorkInProgress bool `json:"work_in_progress"`
		Merged         bool
		User           wireUser
		Author         wireUser
		Head           wireBranch
		Base           wireBranch
		SourceBranch   string `json:"source_branch"`
		TargetBranch   string `json:"target_branch"`
		SHA            string
		DiffRefs       struct {
			Head   string `json:"head_sha"`
			Target string `json:"start_sha"`
			Base   string `json:"base_sha"`
		} `json:"diff_refs"`
		MergeBase           string `json:"merge_base"`
		Mergeable           *bool
		MergeableState      string    `json:"mergeable_state"`
		DetailedMergeStatus string    `json:"detailed_merge_status"`
		ChangedFiles        *int      `json:"changed_files"`
		HTMLURL             string    `json:"html_url"`
		WebURL              string    `json:"web_url"`
		CreatedAt           time.Time `json:"created_at"`
		UpdatedAt           time.Time `json:"updated_at"`
		ClosedAt            time.Time `json:"closed_at"`
		MergedAt            time.Time `json:"merged_at"`
		SourceProjectID     int64     `json:"source_project_id"`
		TargetProjectID     int64     `json:"target_project_id"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return proposal{}, err
	}
	c := ChangeRequest{Ref: Ref{s.ID, path, w.Number}, Kind: s.Session.Kind, ID: strconv.FormatInt(w.ID, 10), Title: w.Title, Description: w.Body,
		Author: w.User.user(), SourceBranch: w.Head.Ref, TargetBranch: w.Base.Ref, Revision: Revision{w.Head.SHA, w.Base.SHA, w.MergeBase},
		State: State(w.State), Draft: w.Draft, WebURL: w.HTMLURL, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, ClosedAt: w.ClosedAt, MergedAt: w.MergedAt}
	if w.Head.Repo != nil {
		c.SourceRepository = w.Head.Repo.FullName
	}
	if w.Base.Repo != nil && w.Base.Repo.FullName != "" {
		c.Ref.Repository = w.Base.Repo.FullName
	}
	status := w.MergeableState
	if s.Session.Kind == forgeauth.GitLab {
		c.Ref.Number = w.IID
		c.Description = w.Description
		c.Author = w.Author.user()
		c.WebURL = w.WebURL
		c.SourceBranch = w.SourceBranch
		c.TargetBranch = w.TargetBranch
		c.Revision = Revision{w.DiffRefs.Head, w.DiffRefs.Target, w.DiffRefs.Base}
		if c.Revision.Head == "" {
			c.Revision.Head = w.SHA
		}
		if w.SourceProjectID != 0 && w.SourceProjectID == w.TargetProjectID {
			c.SourceRepository = path
		}
		c.Draft = w.Draft || w.WorkInProgress
		if w.State == "opened" {
			c.State = Open
		}
		status = w.DetailedMergeStatus
	}
	if w.Merged || !w.MergedAt.IsZero() || w.State == "merged" {
		c.State = Merged
	}
	if c.Ref.Number <= 0 || (c.State != Open && c.State != Closed && c.State != Merged) {
		return proposal{}, fmt.Errorf("changes: invalid proposal response")
	}
	return proposal{c, w.NodeID, w.Mergeable, status, w.ChangedFiles}, nil
}

// draftTitle uses the default GitLab and Forgejo draft prefixes. Native titles
// are retained in results; instance-specific Forgejo prefixes are not guessed.
func draftTitle(title string, draft bool) string {
	for {
		lower := strings.ToLower(title)
		trimmed := false
		for _, p := range []string{"draft:", "wip:", "[draft]", "[wip]", "(draft)", "(wip)"} {
			if strings.HasPrefix(lower, p) {
				title = strings.TrimSpace(title[len(p):])
				trimmed = true
				break
			}
		}
		if !trimmed {
			break
		}
	}
	if draft {
		return "Draft: " + title
	}
	return title
}
