package api

import (
	"net/http"
	"net/url"
	"strconv"

	apb "github.com/r59q/forge-tools/forge-actions/service/actionspb"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type pipelinesData struct {
	Page       string
	Configured bool
	Query      *pb.ListPipelinesRequest
	Listing    *pb.ListPipelinesResponse
	Pipeline   *apb.Pipeline
	Jobs       *pb.ListJobsResponse
	Job        *apb.Job
	Log        *apb.Log
	Problem    string
	LogProblem string
	NextURL    string
}

func pipelineProblem(err error) (int, string) {
	switch status.Code(err) {
	case codes.InvalidArgument:
		return http.StatusBadRequest, "Choose a source, repository and valid filters."
	case codes.NotFound:
		return http.StatusNotFound, "This pipeline, job or log is no longer available."
	case codes.Unimplemented:
		return http.StatusNotImplemented, "This operation is unavailable on the configured forge or connector."
	default:
		return http.StatusServiceUnavailable, "The pipeline service could not complete the request. Try again."
	}
}

func (s *Server) pipelines(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := &pb.ListPipelinesRequest{
		Source:       q.Get("source"),
		Repository:   q.Get("repository"),
		Commit:       q.Get("commit"),
		Branch:       q.Get("branch"),
		DefinitionId: q.Get("definition_id"),
		Phase:        q.Get("phase"),
		Result:       q.Get("result"),
	}
	data := pipelinesData{Page: "pipelines", Configured: s.client.PipelinesConfigured(), Query: query}
	code := http.StatusOK
	page := q.Get("page")
	if page != "" {
		n, err := strconv.ParseInt(page, 10, 32)
		if err != nil || n < 1 {
			code, data.Problem = http.StatusBadRequest, "Choose a valid page number."
		} else {
			query.Page = int32(n)
		}
	}

	if data.Problem == "" && data.Configured && (query.Source != "" || query.Repository != "") {
		var err error
		data.Listing, err = s.ListPipelines(r.Context(), query)
		if err != nil {
			code, data.Problem = pipelineProblem(err)
		} else if data.Listing.NextPage != 0 {
			data.NextURL = pipelineListURL(query, data.Listing.NextPage)
		}
	}

	render(w, r, code, s.views.pipelines, data)
}

func pipelineListURL(query *pb.ListPipelinesRequest, page int32) string {
	values := url.Values{
		"source":        {query.Source},
		"repository":    {query.Repository},
		"branch":        {query.Branch},
		"commit":        {query.Commit},
		"definition_id": {query.DefinitionId},
		"phase":         {query.Phase},
		"result":        {query.Result},
		"page":          {strconv.Itoa(int(page))},
	}

	return "/pipelines?" + values.Encode()
}

func (s *Server) pipeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source, repository, id := q.Get("source"), q.Get("repository"), r.PathValue("id")
	data := pipelinesData{
		Page:       "pipelines",
		Configured: s.client.PipelinesConfigured(),
		Query:      &pb.ListPipelinesRequest{Source: source, Repository: repository},
	}
	code := http.StatusOK
	pipeline, err := s.GetPipeline(r.Context(), &pb.GetPipelineRequest{Source: source, Repository: repository, Id: id})
	data.Pipeline = pipeline.GetPipeline()
	if err == nil {
		data.Jobs, err = s.ListJobs(r.Context(), &pb.ListJobsRequest{Source: source, Repository: repository, Id: id})
	}
	if err != nil {
		code, data.Problem = pipelineProblem(err)
	}

	render(w, r, code, s.views.pipelines, data)
}

func (s *Server) pipelineJob(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source, repository, id := q.Get("source"), q.Get("repository"), r.PathValue("id")
	data := pipelinesData{
		Page:       "pipelines",
		Configured: s.client.PipelinesConfigured(),
		Query:      &pb.ListPipelinesRequest{Source: source, Repository: repository},
	}
	code := http.StatusOK
	job, err := s.GetJob(r.Context(), &pb.GetJobRequest{Source: source, Repository: repository, Id: id})
	data.Job = job.GetJob()
	if err != nil {
		code, data.Problem = pipelineProblem(err)
	} else {
		var jobLog *pb.ReadJobLogResponse
		jobLog, err = s.ReadJobLog(r.Context(), &pb.ReadJobLogRequest{Source: source, Repository: repository, Id: id})
		data.Log = jobLog.GetLog()
		if err != nil {
			_, data.LogProblem = pipelineProblem(err)
		}
	}

	render(w, r, code, s.views.pipelines, data)
}
