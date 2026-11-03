package management

import (
	"context"
	"errors"
	"testing"

	"github.com/r59q/forge-tools/forge-actions/library/actions"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

type pipelineFake struct {
	err   error
	wrong bool
}

func (f pipelineFake) ListPipelines(_ context.Context, o actions.ListOptions) (actions.PipelinePage, error) {
	p := actions.Pipeline{Ref: actions.Ref{RepositoryRef: o.RepositoryRef, ID: "7"}}
	if f.wrong {
		p.Source = "other"
	}

	return actions.PipelinePage{Pipelines: []actions.Pipeline{p}, NextPage: 2}, f.err
}

func (f pipelineFake) GetPipeline(_ context.Context, r actions.Ref) (actions.Pipeline, error) {
	return actions.Pipeline{Ref: r}, f.err
}

func (f pipelineFake) ListJobs(_ context.Context, r actions.Ref) ([]actions.Job, error) {
	return []actions.Job{{Ref: actions.Ref{RepositoryRef: r.RepositoryRef, ID: "9"}, PipelineID: r.ID}}, f.err
}

func (f pipelineFake) GetJob(_ context.Context, r actions.Ref) (actions.Job, error) {
	return actions.Job{Ref: r}, f.err
}

func (f pipelineFake) ReadJobLog(_ context.Context, r actions.Ref) (actions.Log, error) {
	return actions.Log{Text: "log"}, f.err
}

func TestPipelineCapability(t *testing.T) {
	r := actions.Ref{RepositoryRef: actions.RepositoryRef{Source: "source", Repository: "o/r"}, ID: "7"}
	c := New(Config{})
	if list, err := c.ListPipelines(t.Context(), actions.ListOptions{}); err != nil || list.Configured {
		t.Fatalf("absent %+v %v", list, err)
	}
	if _, err := c.GetPipeline(t.Context(), r); !errors.Is(err, ErrNotConfigured) {
		t.Fatal(err)
	}

	c = New(Config{Pipelines: pipelineFake{}})
	page, err := c.ListPipelines(t.Context(), actions.ListOptions{RepositoryRef: r.RepositoryRef})
	if err != nil || !page.Configured || page.Page.NextPage != 2 || len(page.Page.Pipelines) != 1 {
		t.Fatalf("page %+v %v", page, err)
	}
	if _, err := c.GetPipeline(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListJobs(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetJob(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if log, err := c.ReadJobLog(t.Context(), r); err != nil || log.Text != "log" {
		t.Fatal(err)
	}
	if _, err := c.ListPipelines(t.Context(), actions.ListOptions{RepositoryRef: r.RepositoryRef, Phase: "bad"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}

	c = New(Config{Pipelines: pipelineFake{wrong: true}})
	if _, err := c.ListPipelines(t.Context(), actions.ListOptions{RepositoryRef: r.RepositoryRef}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("accepted wrong source", err)
	}

	for _, tc := range []struct{ input, want error }{
		{errors.New("private backend detail"), ErrUnavailable},
		{forgehttp.ErrNotFound, ErrNotFound},
		{actions.ErrUnsupported, ErrPipelineUnsupported},
		{actions.ErrInvalid, ErrInvalid},
	} {
		c := New(Config{Pipelines: pipelineFake{err: tc.input}})
		if p, err := c.GetPipeline(t.Context(), r); !errors.Is(err, tc.want) || p.ID != "" {
			t.Fatalf("failure returned data: %+v %v", p, err)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.ListPipelines(ctx, actions.ListOptions{RepositoryRef: r.RepositoryRef}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
