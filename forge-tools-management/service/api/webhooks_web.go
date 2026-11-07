package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// webhookSecret is a secret shown once, right after a change.
type webhookSecret struct {
	Webhook *pb.Webhook
	Secret  string
	Rotated bool
	Failed  []string
}

type webhooksData struct {
	Listing     *pb.ListWebhooksResponse
	Connections []*pb.Connection
	Failed      bool
	Notice      string
	Problem     string
	Secret      *webhookSecret
	Leftover    []string
	Form        *pb.AddWebhookRequest
	LastEvents  map[string]string
}

// webhookProblem explains a failed webhook change without upstream details.
func webhookProblem(code codes.Code) string {
	switch code {
	case codes.InvalidArgument:
		return "Check the ID (lowercase letters, digits, - and _), the repositories (owner/name) " +
			"and that the connection is a GitHub or Forgejo connection. Nothing was changed."
	case codes.NotFound:
		return "That webhook source or connection no longer exists."
	case codes.AlreadyExists:
		return "A webhook source with that ID already exists. Nothing was changed."
	case codes.FailedPrecondition:
		return "The webhook could not be registered on the forge. Check that forge-reactor has a public URL and a connector, " +
			"and that the connection's token may manage the repositories' webhooks. Nothing was changed."
	case codes.Unimplemented:
		return "No forge-reactor is configured."
	case codes.DeadlineExceeded:
		return "forge-reactor or the forge took too long to answer. Try again."
	default:
		return "forge-reactor is unavailable. Nothing was changed; try again."
	}
}

func (s *Server) webhookPages(mux *http.ServeMux) {
	mux.HandleFunc("GET /webhooks", func(w http.ResponseWriter, r *http.Request) {
		data := webhooksData{}
		if r.URL.Query().Get("done") == "removed" {
			data.Notice = "Webhook source removed. Forges delivering to it now get not found."
		}
		s.webhooks(w, r, http.StatusOK, data)
	})
	mux.HandleFunc("POST /webhooks", s.addWebhook)
	mux.HandleFunc("POST /webhooks/{id}/rotate", s.rotateWebhook)
	mux.HandleFunc("POST /webhooks/{id}/delete", s.removeWebhook)
}

func (s *Server) addWebhook(w http.ResponseWriter, r *http.Request) {
	form := &pb.AddWebhookRequest{
		Id:           strings.TrimSpace(r.PostFormValue("id")),
		Connection:   r.PostFormValue("connection"),
		Repositories: splitRepositories(r.PostFormValue("repositories")),
		Register:     r.PostFormValue("register") == "on",
	}
	created, err := s.AddWebhook(r.Context(), form)
	if err != nil {
		s.webhookFailed(w, r, err, form)

		return
	}

	secret := &webhookSecret{Webhook: created.GetWebhook(), Secret: created.GetSecret()}
	s.webhooks(w, r, http.StatusOK, webhooksData{Secret: secret})
}

func (s *Server) rotateWebhook(w http.ResponseWriter, r *http.Request) {
	rotated, err := s.RotateWebhookSecret(r.Context(), &pb.RotateWebhookSecretRequest{Id: r.PathValue("id")})
	if err != nil {
		s.webhookFailed(w, r, err, nil)

		return
	}

	secret := &webhookSecret{
		Webhook: rotated.GetWebhook(),
		Secret:  rotated.GetSecret(),
		Rotated: true,
		Failed:  rotated.GetFailedRepositories(),
	}
	s.webhooks(w, r, http.StatusOK, webhooksData{Secret: secret})
}

func (s *Server) removeWebhook(w http.ResponseWriter, r *http.Request) {
	removed, err := s.RemoveWebhook(r.Context(), &pb.RemoveWebhookRequest{Id: r.PathValue("id")})
	if err != nil {
		s.webhookFailed(w, r, err, nil)

		return
	}
	if len(removed.GetFailedRepositories()) == 0 {
		http.Redirect(w, r, "/webhooks?done=removed", http.StatusSeeOther)

		return
	}

	s.webhooks(w, r, http.StatusOK, webhooksData{Leftover: removed.GetFailedRepositories()})
}

// webhookFailed shows the page with a fixed problem description.
func (s *Server) webhookFailed(w http.ResponseWriter, r *http.Request, err error, form *pb.AddWebhookRequest) {
	code := status.Code(err)
	s.webhooks(w, r, runtime.HTTPStatusFromCode(code), webhooksData{Problem: webhookProblem(code), Form: form})
}

func (s *Server) webhooks(w http.ResponseWriter, r *http.Request, code int, data webhooksData) {
	// Keep the request's credential but start a fresh deadline, so a slow
	// change still shows current state.
	ctx := context.WithoutCancel(r.Context())
	listing, err := s.ListWebhooks(ctx, nil)
	if err != nil && code == http.StatusOK {
		code = http.StatusServiceUnavailable
	}
	if connections, err := s.ListConnections(ctx, nil); err == nil {
		data.Connections = webhookConnections(connections.GetConnections())
	}
	if data.Form == nil {
		data.Form = &pb.AddWebhookRequest{}
	}

	data.Listing, data.Failed = listing, err != nil
	data.LastEvents = s.lastEvents(ctx, listing.GetWebhooks())
	render(w, r, code, s.views.webhooks, struct {
		Page string
		webhooksData
	}{"webhooks", data})
}

// webhookConnections keeps the connections forge-reactor can serve.
func webhookConnections(all []*pb.Connection) []*pb.Connection {
	var out []*pb.Connection
	for _, c := range all {
		if c.GetForge() == "github" || c.GetForge() == "forgejo" {
			out = append(out, c)
		}
	}

	return out
}

// splitRepositories reads owner/name paths separated by commas or spaces.
func splitRepositories(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})
}
