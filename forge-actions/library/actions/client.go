package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// Client reads automation through its sources. It is safe for concurrent use
// when the supplied sessions are. It holds no caches or forge credentials.
type Client struct {
	sources []Source
	byID    map[string]Source
}

// New validates sources. An empty client is valid.
func New(sources ...Source) (*Client, error) {
	c := &Client{byID: map[string]Source{}}
	for _, s := range sources {
		if strings.TrimSpace(s.ID) == "" || s.Session == nil || s.Session.Client == nil {
			return nil, ErrInvalid
		}
		if _, found := c.byID[s.ID]; found {
			return nil, ErrInvalid
		}

		switch s.Session.Kind {
		case forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo:
		default:
			return nil, ErrUnsupported
		}

		c.byID[s.ID] = s
		c.sources = append(c.sources, s)
	}

	return c, nil
}

// Sources lists source IDs, forge kinds and adapter capabilities.
func (c *Client) Sources() []SourceInfo {
	out := make([]SourceInfo, 0, len(c.sources))
	for _, s := range c.sources {
		out = append(out, SourceInfo{
			ID:   s.ID,
			Kind: s.Session.Kind,
			Capabilities: Capabilities{
				Definitions: s.Session.Kind == forgeauth.GitHub,
				Steps:       s.Session.Kind != forgeauth.GitLab,
				Stages:      s.Session.Kind == forgeauth.GitLab,
				JobLogs:     true,
				Artifacts:   true,
			},
		})
	}

	return out
}

func (c *Client) resolve(r RepositoryRef) (Source, string, error) {
	path, ok := forgerest.CleanPath(r.Repository)
	if !ok || r.Source == "" {
		return Source{}, "", ErrInvalid
	}

	s, ok := c.byID[r.Source]
	if !ok {
		return Source{}, "", ErrUnknownSource
	}
	if s.Session.Kind == forgeauth.GitLab {
		return s, "/projects/" + url.PathEscape(path), nil
	}

	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		return Source{}, "", ErrInvalid
	}

	return s, "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]), nil
}

func validID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func (c *Client) resolveID(r Ref) (Source, string, error) {
	if !validID(r.ID) {
		return Source{}, "", ErrInvalid
	}

	return c.resolve(r.RepositoryRef)
}

// ValidateList checks a pipeline query without contacting a forge.
func ValidateList(o ListOptions) error {
	if _, ok := forgerest.CleanPath(o.Repository); !ok || o.Source == "" || o.Page < 0 || o.Page > 1000000 {
		return ErrInvalid
	}
	if !oneOf(
		o.Phase,
		"",
		phaseUnknown,
		phaseQueued,
		phaseRunning,
		phaseWaiting,
		phaseFinished,
	) || !oneOf(
		o.Result,
		"",
		"success",
		"failure",
		"cancelled",
		"skipped",
		"neutral",
		"unknown",
	) {
		return ErrInvalid
	}

	for _, v := range []string{o.Commit, o.Branch, o.DefinitionID} {
		if len(v) > 1024 || strings.ContainsAny(v, "\x00\r\n") {
			return ErrInvalid
		}
	}

	return nil
}

// ListPipelines reads one bounded page. Filtering does not change pagination.
func (c *Client) ListPipelines(ctx context.Context, o ListOptions) (PipelinePage, error) {
	if err := ValidateList(o); err != nil {
		return PipelinePage{}, err
	}

	s, base, err := c.resolve(o.RepositoryRef)
	if err != nil {
		return PipelinePage{}, err
	}

	page := max(1, o.Page)
	if s.Session.Kind == forgeauth.GitHub && page > 20 && (o.Commit != "" || o.Branch != "") {
		return PipelinePage{}, ErrTooLarge
	}

	q := url.Values{}
	path, envelope := base+"/actions/runs", "workflow_runs"
	switch s.Session.Kind {
	case forgeauth.GitLab:
		if o.DefinitionID != "" {
			return PipelinePage{}, ErrUnsupported
		}
		path, envelope = base+"/pipelines", ""
		q.Set("sha", o.Commit)
		q.Set("ref", o.Branch)
		q.Set("order_by", "id")
		q.Set("sort", "desc")
	case forgeauth.GitHub:
		q.Set("head_sha", o.Commit)
		q.Set("branch", o.Branch)
		if o.DefinitionID != "" {
			if !validID(o.DefinitionID) {
				return PipelinePage{}, ErrInvalid
			}

			path = base + "/actions/workflows/" + o.DefinitionID + "/runs"
		}
	case forgeauth.Forgejo:
		q.Set("head_sha", o.Commit)
		q.Set("workflow_id", o.DefinitionID)
		if o.Branch != "" {
			q.Set("ref", "refs/heads/"+o.Branch)
		}
	}

	items, more, err := readPage(ctx, s, path, q, envelope, page)
	if err != nil {
		return PipelinePage{}, err
	}

	out := PipelinePage{Pipelines: []Pipeline{}}
	for _, item := range items {
		p, err := decodePipeline(s.Session.Kind, item)
		if err != nil {
			return PipelinePage{}, err
		}

		p.RepositoryRef = o.RepositoryRef
		if (o.Phase == "" || p.Status.Phase == o.Phase) && (o.Result == "" || p.Status.Result == o.Result) {
			out.Pipelines = append(out.Pipelines, p)
		}
	}

	if more {
		out.NextPage = page + 1
	}

	return out, nil
}

// GetPipeline reads a pipeline by its native execution ID.
func (c *Client) GetPipeline(ctx context.Context, r Ref) (Pipeline, error) {
	s, base, err := c.resolveID(r)
	if err != nil {
		return Pipeline{}, err
	}

	path := base + "/actions/runs/" + r.ID
	if s.Session.Kind == forgeauth.GitLab {
		path = base + "/pipelines/" + r.ID
	}

	var raw json.RawMessage
	if err := forgerest.Send(ctx, s.Session, "GET", path, nil, &raw); err != nil {
		return Pipeline{}, err
	}

	p, err := decodePipeline(s.Session.Kind, raw)
	if err == nil && p.ID != r.ID {
		return Pipeline{}, fmt.Errorf("actions: mismatched pipeline ID")
	}

	p.RepositoryRef = r.RepositoryRef

	return p, err
}

// ListJobs lists the latest jobs of a pipeline, up to 1000. GitLab trigger
// jobs and downstream pipelines are not included; no dependency graph is inferred.
func (c *Client) ListJobs(ctx context.Context, r Ref) ([]Job, error) {
	s, base, err := c.resolveID(r)
	if err != nil {
		return nil, err
	}

	path, envelope := base+"/actions/runs/"+r.ID+"/jobs", "jobs"
	q := url.Values{"filter": {"latest"}}
	switch s.Session.Kind {
	case forgeauth.GitLab:
		path, envelope, q = base+"/pipelines/"+r.ID+"/jobs", "", url.Values{"include_retried": {"false"}}
	case forgeauth.Forgejo:
		envelope = ""
	}

	var items []json.RawMessage
	if s.Session.Kind == forgeauth.Forgejo {
		// This Forgejo endpoint returns an unpaged array.
		err = forgerest.Send(ctx, s.Session, "GET", path, nil, &items)
		if len(items) > 1000 {
			return nil, ErrTooLarge
		}
		if err == nil && items == nil {
			return nil, fmt.Errorf("actions: missing jobs array")
		}
	} else {
		items, err = readAll(ctx, s, path, q, envelope)
	}

	if err != nil {
		return nil, err
	}

	out := make([]Job, 0, len(items))
	for _, raw := range items {
		j, err := decodeJob(s.Session.Kind, raw)
		if err != nil {
			return nil, err
		}

		j.RepositoryRef = r.RepositoryRef
		if j.PipelineID != "" && j.PipelineID != r.ID {
			return nil, fmt.Errorf("actions: mismatched job pipeline")
		}

		j.PipelineID = r.ID
		out = append(out, j)
	}

	return out, nil
}

// GetJob reads one job, including steps where supplied by the forge.
func (c *Client) GetJob(ctx context.Context, r Ref) (Job, error) {
	s, base, err := c.resolveID(r)
	if err != nil {
		return Job{}, err
	}

	path := base + "/actions/jobs/" + r.ID
	if s.Session.Kind == forgeauth.GitLab {
		path = base + "/jobs/" + r.ID
	}

	var raw json.RawMessage
	if err := forgerest.Send(ctx, s.Session, "GET", path, nil, &raw); err != nil {
		return Job{}, err
	}

	j, err := decodeJob(s.Session.Kind, raw)
	if err == nil && j.ID != r.ID {
		return Job{}, fmt.Errorf("actions: mismatched job ID")
	}

	j.RepositoryRef = r.RepositoryRef

	return j, err
}

// ReadJobLog reads up to 1 MiB of the latest job log. Missing logs retain the
// forge's not-found error; redirects blocked by the connector are unsupported.
func (c *Client) ReadJobLog(ctx context.Context, r Ref) (Log, error) {
	s, base, err := c.resolveID(r)
	if err != nil {
		return Log{}, err
	}

	path := base + "/actions/jobs/" + r.ID + "/logs"
	if s.Session.Kind == forgeauth.GitLab {
		path = base + "/jobs/" + r.ID + "/trace"
	}

	data, over, err := readDownload(ctx, s, path, 1<<20)

	return Log{Text: strings.ToValidUTF8(string(data), "�"), Truncated: over}, err
}

// ListDefinitions reads GitHub's workflow catalog, up to 1000 entries.
// Other adapters return ErrUnsupported rather than inventing definitions.
func (c *Client) ListDefinitions(ctx context.Context, r RepositoryRef) ([]Definition, error) {
	s, base, err := c.resolve(r)
	if err != nil {
		return nil, err
	}
	if s.Session.Kind != forgeauth.GitHub {
		return nil, ErrUnsupported
	}

	items, err := readAll(ctx, s, base+"/actions/workflows", nil, "workflows")
	if err != nil {
		return nil, err
	}

	out := make([]Definition, 0, len(items))
	for _, raw := range items {
		var v struct {
			ID     int64
			Name   string
			Path   string
			State  string
			WebURL string `json:"html_url"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if v.ID <= 0 {
			return nil, fmt.Errorf("actions: missing definition ID")
		}

		out = append(out, Definition{
			ID:     strconv.FormatInt(v.ID, 10),
			Name:   v.Name,
			Path:   v.Path,
			State:  v.State,
			WebURL: v.WebURL,
		})
	}

	return out, nil
}

func oneOf(v string, values ...string) bool {
	for _, s := range values {
		if v == s {
			return true
		}
	}

	return false
}
