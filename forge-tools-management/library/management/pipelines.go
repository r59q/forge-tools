package management

import (
	"context"
	"errors"
	"strconv"

	"github.com/r59q/forge-tools/forge-actions/library/actions"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// PipelineReader supplies pipeline browsing and job logs.
type PipelineReader interface {
	ListPipelines(context.Context, actions.ListOptions) (actions.PipelinePage, error)
	GetPipeline(context.Context, actions.Ref) (actions.Pipeline, error)
	ListJobs(context.Context, actions.Ref) ([]actions.Job, error)
	GetJob(context.Context, actions.Ref) (actions.Job, error)
	ReadJobLog(context.Context, actions.Ref) (actions.Log, error)
}

// PipelineListing distinguishes a missing capability from an empty page.
type PipelineListing struct {
	Configured bool
	Page       actions.PipelinePage
}

// PipelinesConfigured reports whether pipeline browsing is configured.
func (c *Client) PipelinesConfigured() bool { return c.pipelines != nil }

// ListPipelines reads one page. Errors discard results and upstream details.
func (c *Client) ListPipelines(ctx context.Context, q actions.ListOptions) (PipelineListing, error) {
	if err := ctx.Err(); err != nil {
		return PipelineListing{}, err
	}
	if c.pipelines == nil {
		return PipelineListing{}, nil
	}
	if err := actions.ValidateList(q); err != nil {
		return PipelineListing{}, ErrInvalid
	}

	page, err := c.pipelines.ListPipelines(ctx, q)
	if err != nil {
		return PipelineListing{}, pipelineError(ctx, err)
	}
	if len(page.Pipelines) > 50 || page.NextPage != 0 && page.NextPage != max(1, q.Page)+1 {
		return PipelineListing{}, ErrUnavailable
	}

	for _, p := range page.Pipelines {
		if p.Source != q.Source || p.Repository != q.Repository || !validPipelineID(p.ID) {
			return PipelineListing{}, ErrUnavailable
		}
	}

	return PipelineListing{Configured: true, Page: page}, nil
}

func validPipelineID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func (c *Client) checkPipeline(ctx context.Context, r actions.Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.pipelines == nil {
		return ErrNotConfigured
	}
	if !validPipelineID(r.ID) || actions.ValidateList(actions.ListOptions{RepositoryRef: r.RepositoryRef}) != nil {
		return ErrInvalid
	}

	return nil
}

// GetPipeline reads one execution from the configured pipeline service.
func (c *Client) GetPipeline(ctx context.Context, r actions.Ref) (actions.Pipeline, error) {
	if err := c.checkPipeline(ctx, r); err != nil {
		return actions.Pipeline{}, err
	}

	p, err := c.pipelines.GetPipeline(ctx, r)
	if err != nil {
		return actions.Pipeline{}, pipelineError(ctx, err)
	}
	if p.Ref != r {
		return actions.Pipeline{}, ErrUnavailable
	}

	return p, nil
}

// ListJobs reads the latest jobs of an execution.
func (c *Client) ListJobs(ctx context.Context, r actions.Ref) ([]actions.Job, error) {
	if err := c.checkPipeline(ctx, r); err != nil {
		return nil, err
	}

	jobs, err := c.pipelines.ListJobs(ctx, r)
	if err != nil {
		return nil, pipelineError(ctx, err)
	}

	for _, j := range jobs {
		if j.RepositoryRef != r.RepositoryRef || j.PipelineID != r.ID || !validPipelineID(j.ID) {
			return nil, ErrUnavailable
		}
	}

	return jobs, nil
}

// GetJob reads one job, including native steps when available.
func (c *Client) GetJob(ctx context.Context, r actions.Ref) (actions.Job, error) {
	if err := c.checkPipeline(ctx, r); err != nil {
		return actions.Job{}, err
	}

	j, err := c.pipelines.GetJob(ctx, r)
	if err != nil {
		return actions.Job{}, pipelineError(ctx, err)
	}
	if j.Ref != r {
		return actions.Job{}, ErrUnavailable
	}

	return j, nil
}

// ReadJobLog returns a bounded job log snapshot.
func (c *Client) ReadJobLog(ctx context.Context, r actions.Ref) (actions.Log, error) {
	if err := c.checkPipeline(ctx, r); err != nil {
		return actions.Log{}, err
	}

	l, err := c.pipelines.ReadJobLog(ctx, r)
	if err != nil {
		return actions.Log{}, pipelineError(ctx, err)
	}

	return l, nil
}

func pipelineError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	switch {
	case errors.Is(err, actions.ErrInvalid), errors.Is(err, actions.ErrUnknownSource):
		return ErrInvalid
	case errors.Is(err, forgehttp.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, actions.ErrUnsupported):
		return ErrPipelineUnsupported
	default:
		return ErrUnavailable
	}
}

// ErrPipelineUnsupported means the forge or connector lacks this operation.
var ErrPipelineUnsupported = errors.New("management: pipeline operation unsupported")
