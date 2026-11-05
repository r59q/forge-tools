package webhook

import (
	"cmp"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

// repository is the repository object both forges send.
type repository struct {
	FullName string `json:"full_name"`
}

// user is a user object; GitHub pushers have a name, others a login.
type user struct {
	Login    string `json:"login"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// pushPayload holds the push fields both forges share.
type pushPayload struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Created    bool   `json:"created"`
	Deleted    bool   `json:"deleted"`
	Forced     bool   `json:"forced"`
	Repository struct {
		FullName string          `json:"full_name"`
		PushedAt json.RawMessage `json:"pushed_at"`
	} `json:"repository"`
	Pusher user `json:"pusher"`
}

// pullPayload holds the pull request fields both forges share.
type pullPayload struct {
	Action      string     `json:"action"`
	Number      int64      `json:"number"`
	Repository  repository `json:"repository"`
	PullRequest struct {
		Number    int64   `json:"number"`
		Title     string  `json:"title"`
		HTMLURL   string  `json:"html_url"`
		Draft     bool    `json:"draft"`
		User      user    `json:"user"`
		CreatedAt string  `json:"created_at"`
		Head      pullRef `json:"head"`
		Base      pullRef `json:"base"`
	} `json:"pull_request"`
}

// pullRef is one side of a pull request.
type pullRef struct {
	Ref  string      `json:"ref"`
	SHA  string      `json:"sha"`
	Repo *repository `json:"repo"`
}

// push normalizes a push. Only pushes that move an existing branch produce
// an event.
func push(body []byte) (*reactor.Event, string, error) {
	var p pushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, "", ErrMalformed
	}

	branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
	switch {
	case !ok:
		return nil, "not a branch push", nil
	case p.Created || zero(p.Before):
		return nil, "branch created", nil
	case p.Deleted || zero(p.After):
		return nil, "branch deleted", nil
	case branch == "" || p.After == "" || p.Repository.FullName == "":
		return nil, "", ErrMalformed
	}

	e := &reactor.Event{
		Type:       reactor.TypeBranchUpdated,
		Repository: p.Repository.FullName,
		Subject:    p.Ref,
		OccurredAt: unixTime(p.Repository.PushedAt),
	}
	e.Data, _ = json.Marshal(reactor.BranchUpdate{
		Branch: branch,
		Before: p.Before,
		After:  p.After,
		Forced: p.Forced,
		Pusher: cmp.Or(p.Pusher.Login, p.Pusher.Username, p.Pusher.Name),
	})

	return e, "", nil
}

// pullRequest normalizes a pull request delivery. Only openings produce an
// event.
func pullRequest(body []byte) (*reactor.Event, string, error) {
	var p pullPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, "", ErrMalformed
	}
	if p.Action != "opened" {
		return nil, "pull request action " + p.Action, nil
	}

	pr := p.PullRequest
	number := cmp.Or(pr.Number, p.Number)
	if number <= 0 || p.Repository.FullName == "" {
		return nil, "", ErrMalformed
	}

	e := &reactor.Event{
		Type:       reactor.TypePullRequestOpened,
		Repository: p.Repository.FullName,
		Subject:    "pulls/" + strconv.FormatInt(number, 10),
	}
	if at, err := time.Parse(time.RFC3339, pr.CreatedAt); err == nil {
		e.OccurredAt = at.UTC()
	}
	e.Data, _ = json.Marshal(reactor.PullRequest{
		Number: number,
		Title:  pr.Title,
		URL:    pr.HTMLURL,
		Author: pr.User.Login,
		Draft:  pr.Draft,
		Head:   side(pr.Head),
		Base:   side(pr.Base),
	})

	return e, "", nil
}

// side converts one pull request side.
func side(r pullRef) reactor.Ref {
	out := reactor.Ref{Branch: r.Ref, Commit: r.SHA}
	if r.Repo != nil {
		out.Repository = r.Repo.FullName
	}

	return out
}

// zero reports an all-zero commit ID, which marks a created or deleted ref.
func zero(id string) bool { return id != "" && strings.Trim(id, "0") == "" }

// unixTime reads a Unix-seconds timestamp, as GitHub sends in push
// repositories; other forms are unknown.
func unixTime(raw json.RawMessage) time.Time {
	var seconds int64
	if json.Unmarshal(raw, &seconds) != nil || seconds <= 0 {
		return time.Time{}
	}

	return time.Unix(seconds, 0).UTC()
}
