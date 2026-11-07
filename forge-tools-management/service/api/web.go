package api

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

//go:embed layout.html repositories.html issues.html connections.html pipelines.html webhooks.html style.css
var files embed.FS

// views holds the parsed web pages.
type views struct {
	repositories *template.Template
	issues       *template.Template
	connections  *template.Template
	pipelines    *template.Template
	webhooks     *template.Template
}

// parseViews parses the embedded pages. They never change, so a failure is a
// bug and panics.
func parseViews() views {
	page := func(name string) *template.Template {
		return template.Must(template.ParseFS(files, "layout.html", name+".html"))
	}
	helpers := template.FuncMap{
		"phases":  func() []string { return []string{"queued", "running", "waiting", "finished", "unknown"} },
		"results": func() []string { return []string{"success", "failure", "cancelled", "skipped", "neutral", "unknown"} },
		"join":    func(values []string) string { return strings.Join(values, ", ") },
	}

	return views{
		repositories: page("repositories"),
		issues:       page("issues"),
		connections:  page("connections"),
		pipelines:    template.Must(template.New("pipelines").Funcs(helpers).ParseFS(files, "layout.html", "pipelines.html")),
		webhooks:     template.Must(template.New("webhooks").Funcs(helpers).ParseFS(files, "layout.html", "webhooks.html")),
	}
}

// notice returns the message for a finished change. Redirects can show only
// these; query text is never rendered.
func notice(done string) string {
	switch done {
	case "added":
		return "Connection added. Tools can use it right away."
	case "tested":
		return "Connection checked. The forge accepted its token."
	case "rotated":
		return "Token replaced. The forge accepted the new token."
	case "removed":
		return "Connection removed. Its token was deleted."
	default:
		return ""
	}
}

// problem explains a failed connection change without upstream details.
func problem(code codes.Code) string {
	switch code {
	case codes.InvalidArgument:
		return "The forge rejected the token, or the input is invalid. Nothing was changed."
	case codes.NotFound:
		return "That connection no longer exists."
	case codes.FailedPrecondition:
		return "The check failed: the forge rejected the stored token or could not be reached. " +
			"The connection is marked as not working until a check or a new token succeeds."
	case codes.Unimplemented:
		return "No connector is configured."
	case codes.DeadlineExceeded:
		return "The forge or connector took too long to answer. Try again."
	default:
		return "The connector is unavailable. Nothing was changed; try again."
	}
}

type connectionsData struct {
	Listing *pb.ListConnectionsResponse
	Failed  bool
	Notice  string
	Problem string
	Form    *pb.AddConnectionRequest
}

func (s *Server) pages() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.repositories)
	mux.HandleFunc("GET /issues", s.issues)
	mux.HandleFunc("GET /pipelines", s.pipelines)
	mux.HandleFunc("GET /pipelines/{id}", s.pipeline)
	mux.HandleFunc("GET /pipeline-jobs/{id}", s.pipelineJob)
	mux.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, files, "style.css")
	})
	mux.HandleFunc("GET /connections", func(w http.ResponseWriter, r *http.Request) {
		s.connections(w, r, http.StatusOK, connectionsData{Notice: notice(r.URL.Query().Get("done"))})
	})
	mux.HandleFunc("POST /connections", func(w http.ResponseWriter, r *http.Request) {
		form := &pb.AddConnectionRequest{
			Forge:      r.PostFormValue("forge"),
			Host:       r.PostFormValue("host"),
			Name:       r.PostFormValue("name"),
			Credential: r.PostFormValue("credential"),
		}
		_, err := s.AddConnection(r.Context(), form)
		form.Credential = ""
		s.changed(w, r, err, "added", form)
	})
	mux.HandleFunc("POST /connections/{id}/test", func(w http.ResponseWriter, r *http.Request) {
		_, err := s.TestConnection(r.Context(), &pb.TestConnectionRequest{Id: r.PathValue("id")})
		s.changed(w, r, err, "tested", nil)
	})
	mux.HandleFunc("POST /connections/{id}/credential", func(w http.ResponseWriter, r *http.Request) {
		_, err := s.RotateCredential(r.Context(), &pb.RotateCredentialRequest{Id: r.PathValue("id"), Credential: r.PostFormValue("credential")})
		s.changed(w, r, err, "rotated", nil)
	})
	mux.HandleFunc("POST /connections/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		_, err := s.RemoveConnection(r.Context(), &pb.RemoveConnectionRequest{Id: r.PathValue("id")})
		s.changed(w, r, err, "removed", nil)
	})
	s.webhookPages(mux)

	return mux
}

// web authenticates browser requests with HTTP Basic before routing them.
func (s *Server) web(pages http.Handler, w http.ResponseWriter, r *http.Request) {
	// Replace incoming metadata with only the Basic credential. Neither proxy
	// headers nor query strings can supply operator authentication.
	user, password, ok := r.BasicAuth()
	ctx := metadata.NewIncomingContext(r.Context(), metadata.MD{})
	if ok && user == "operator" {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+password))
	}
	if err := servicekit.Authorize(ctx, s.token); err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="forge-tools-management", charset="UTF-8"`)
		http.Error(w, "Operator authentication required", http.StatusUnauthorized)

		return
	}

	pages.ServeHTTP(w, r.WithContext(ctx))
}

func (s *Server) repositories(w http.ResponseWriter, r *http.Request) {
	listing, err := s.ListRepositories(r.Context(), nil)
	code := http.StatusOK
	if err != nil {
		code = runtime.HTTPStatusFromCode(status.Code(err))
		if code != http.StatusUnauthorized {
			code = http.StatusServiceUnavailable
		}
	}

	render(w, r, code, s.views.repositories, struct {
		Page      string
		Listing   *pb.ListRepositoriesResponse
		Failed    bool
		Issues    bool
		Pipelines bool
	}{"repositories", listing, err != nil, s.client.IssuesConfigured(), s.client.PipelinesConfigured()})
}

func (s *Server) issues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := &pb.ListIssuesRequest{
		Scope:      q.Get("scope"),
		State:      q.Get("state"),
		Source:     q.Get("source"),
		Repository: q.Get("repository"),
	}
	listing, err := s.ListIssues(r.Context(), query)
	code := http.StatusOK
	invalid := status.Code(err) == codes.InvalidArgument
	if err != nil {
		code = runtime.HTTPStatusFromCode(status.Code(err))
		if code != http.StatusUnauthorized && !invalid {
			code = http.StatusServiceUnavailable
		}
	}

	render(w, r, code, s.views.issues, struct {
		Page    string
		Query   *pb.ListIssuesRequest
		Listing *pb.ListIssuesResponse
		Failed  bool
		Invalid bool
	}{"issues", query, listing, err != nil, invalid})
}

// changed redirects after a successful change, so a reload does not repeat
// it, or shows the connections page with a fixed problem description.
func (s *Server) changed(w http.ResponseWriter, r *http.Request, err error, done string, form *pb.AddConnectionRequest) {
	if err == nil {
		http.Redirect(w, r, "/connections?done="+done, http.StatusSeeOther)
		return
	}

	code := status.Code(err)
	s.connections(w, r, runtime.HTTPStatusFromCode(code), connectionsData{Problem: problem(code), Form: form})
}

func (s *Server) connections(w http.ResponseWriter, r *http.Request, code int, data connectionsData) {
	// Keep the request's credential but start a fresh deadline, so a slow
	// change still shows current state.
	listing, err := s.ListConnections(context.WithoutCancel(r.Context()), nil)
	if err != nil && code == http.StatusOK {
		code = http.StatusServiceUnavailable
	}
	if data.Form == nil {
		data.Form = &pb.AddConnectionRequest{Forge: "github"}
	}

	data.Listing, data.Failed = listing, err != nil
	render(w, r, code, s.views.connections, struct {
		Page string
		connectionsData
	}{"connections", data})
}

func render(w http.ResponseWriter, r *http.Request, code int, page *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_ = page.ExecuteTemplate(w, "layout", data)
	}
}
