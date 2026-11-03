package api

import (
	"context"
	"errors"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-actions/library/actions"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestPipelinePaginationPreservesFilters(t *testing.T) {
	s, _, _ := setupConfig(t, management.Config{Pipelines: pipelineReader{}}, time.Second)
	filters := url.Values{
		"source":        {"source"},
		"repository":    {"group/repo"},
		"branch":        {"feature/one&two"},
		"commit":        {"abc"},
		"definition_id": {"ci.yml"},
		"phase":         {"running"},
		"result":        {"failure"},
	}

	response := page(t, s, "/pipelines?"+filters.Encode())
	if response.Code != 200 {
		t.Fatalf("page failed: %d %s", response.Code, response.Body)
	}

	link := regexp.MustCompile(`<a href="([^"]+)">Next page</a>`).FindStringSubmatch(response.Body.String())
	if len(link) != 2 {
		t.Fatal("next-page link is missing")
	}

	target, err := url.Parse(html.UnescapeString(link[1]))
	if err != nil {
		t.Fatal(err)
	}
	if target.Path != "/pipelines" || target.Query().Get("page") != "2" {
		t.Fatalf("unexpected page target: %s", target)
	}

	for key := range filters {
		if target.Query().Get(key) != filters.Get(key) {
			t.Errorf("filter %s was lost or changed", key)
		}
	}
}

type pipelineReader struct {
	err    error
	logErr error
}

func (f pipelineReader) ListPipelines(_ context.Context, o actions.ListOptions) (actions.PipelinePage, error) {
	return actions.PipelinePage{Pipelines: []actions.Pipeline{{
		Ref:    actions.Ref{RepositoryRef: o.RepositoryRef, ID: "7"},
		Name:   "<script>pipeline</script>",
		Status: actions.Status{Phase: "running"},
	}}, NextPage: 2}, f.err
}

func (f pipelineReader) GetPipeline(_ context.Context, r actions.Ref) (actions.Pipeline, error) {
	return actions.Pipeline{Ref: r, Name: "Build", Status: actions.Status{Phase: "running"}}, f.err
}

func (f pipelineReader) ListJobs(_ context.Context, r actions.Ref) ([]actions.Job, error) {
	return []actions.Job{{
		Ref:        actions.Ref{RepositoryRef: r.RepositoryRef, ID: "9"},
		PipelineID: r.ID,
		Name:       "Test",
		Status:     actions.Status{Phase: "running"},
	}}, f.err
}

func (f pipelineReader) GetJob(_ context.Context, r actions.Ref) (actions.Job, error) {
	return actions.Job{
		Ref:        r,
		PipelineID: "7",
		Name:       "Test",
		StepsKnown: true,
		Steps:      []actions.Step{{Name: "Compile", Status: actions.Status{Phase: "finished", Result: "success"}}},
		Status:     actions.Status{Phase: "running"},
	}, f.err
}

func (f pipelineReader) ReadJobLog(context.Context, actions.Ref) (actions.Log, error) {
	return actions.Log{Text: "<script>log</script>", Truncated: true}, f.logErr
}

func TestPipelineBrowsing(t *testing.T) {
	s, h, client := setupConfig(t, management.Config{Pipelines: pipelineReader{}}, time.Second)
	ctx, cancel := rpcContext()
	defer cancel()
	got, err := client.ListPipelines(ctx, &pb.ListPipelinesRequest{Source: "source", Repository: "o/r"})
	if err != nil {
		t.Fatal(err)
	}

	w := request(h, "/v1/pipelines?source=source&repository=o/r", operatorToken)
	var rest pb.ListPipelinesResponse
	if w.Code != 200 || protojson.Unmarshal(w.Body.Bytes(), &rest) != nil || !proto.Equal(got, &rest) {
		t.Fatalf("REST/gRPC mismatch %d %s", w.Code, w.Body)
	}

	for _, tc := range []struct{ path, want string }{
		{"/pipelines", "Choose a repository"},
		{"/pipelines?source=source&repository=o/r", "&lt;script&gt;pipeline&lt;/script&gt;"},
		{"/pipelines/7?source=source&repository=o/r", "/pipeline-jobs/9?"},
		{"/pipeline-jobs/9?source=source&repository=o/r", "&lt;script&gt;log&lt;/script&gt;"},
	} {
		w := page(t, s, tc.path)
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.want) || strings.Contains(w.Body.String(), "<script>") {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}

	w = page(t, s, "/pipelines?source=source&repository=o/r&page=bad")
	if w.Code != 400 {
		t.Fatal(w.Code)
	}

	w = page(t, s, "/pipeline-jobs/9?source=source&repository=o/r")
	if !strings.Contains(w.Body.String(), "first part of the log") || !strings.Contains(w.Body.String(), "Compile") {
		t.Fatal("missing truncation or steps")
	}
}

func TestPipelineRESTReturnsBareResources(t *testing.T) {
	_, h, client := setupConfig(t, management.Config{Pipelines: pipelineReader{}}, time.Second)
	ctx, cancel := rpcContext()
	defer cancel()

	pipeline, err := client.GetPipeline(ctx, &pb.GetPipelineRequest{Source: "source", Repository: "o/r", Id: "7"})
	if err != nil {
		t.Fatal(err)
	}

	job, err := client.GetJob(ctx, &pb.GetJobRequest{Source: "source", Repository: "o/r", Id: "9"})
	if err != nil {
		t.Fatal(err)
	}

	log, err := client.ReadJobLog(ctx, &pb.ReadJobLogRequest{Source: "source", Repository: "o/r", Id: "9"})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path string
		want proto.Message
	}{
		{"/v1/pipelines/7?source=source&repository=o/r", pipeline.GetPipeline()},
		{"/v1/jobs/9?source=source&repository=o/r", job.GetJob()},
		{"/v1/jobs/9/log?source=source&repository=o/r", log.GetLog()},
	} {
		w := request(h, tc.path, operatorToken)
		rest := tc.want.ProtoReflect().New().Interface()
		if w.Code != 200 || protojson.Unmarshal(w.Body.Bytes(), rest) != nil || !proto.Equal(tc.want, rest) {
			t.Fatalf("%s: REST/gRPC mismatch %d %s", tc.path, w.Code, w.Body)
		}
	}
}

func TestPipelineAbsentFailedAndMissingLog(t *testing.T) {
	for _, tc := range []struct {
		reader     management.PipelineReader
		path, want string
		code       int
	}{
		{nil, "/pipelines", "No pipeline capability configured", 200},
		{pipelineReader{err: errors.New("backend-secret-sentinel")}, "/pipelines?source=source&repository=o/r", "Pipeline request failed", 503},
		{pipelineReader{logErr: forgehttp.ErrNotFound}, "/pipeline-jobs/9?source=source&repository=o/r", "no longer available", 200},
		{pipelineReader{logErr: actions.ErrUnsupported}, "/pipeline-jobs/9?source=source&repository=o/r", "operation is unavailable", 200},
	} {
		s, _, _ := setupConfig(t, management.Config{Pipelines: tc.reader}, time.Second)
		w := page(t, s, tc.path)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) || strings.Contains(w.Body.String(), "sentinel") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
}
