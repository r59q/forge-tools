package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	rpb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

// timeLayout formats event times.
const timeLayout = "2006-01-02 15:04:05 MST"

type eventsData struct {
	Page    string
	Query   *pb.ListEventsRequest
	Listing *pb.ListEventsResponse
	Problem string
	NextURL string
}

type eventData struct {
	Page       string
	Event      *rpb.Event
	Deliveries []*rpb.Delivery
	Data       string
	Payload    string
	Problem    string
}

// eventProblem explains a failed event read without upstream details.
func eventProblem(code codes.Code) string {
	switch code {
	case codes.InvalidArgument:
		return "Check the filters: a type looks like forge.branch.updated and a repository like owner/name."
	case codes.NotFound:
		return "This event is no longer retained. Events are kept for 7 days, up to the newest 1000."
	case codes.Unimplemented:
		return "No forge-reactor is configured."
	default:
		return "forge-reactor could not be reached. Check that it is running, then refresh."
	}
}

func (s *Server) eventPages(mux *http.ServeMux) {
	mux.HandleFunc("GET /events", s.events)
	mux.HandleFunc("GET /events/{id}", s.event)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := &pb.ListEventsRequest{
		Source:     q.Get("source"),
		Type:       q.Get("type"),
		Repository: q.Get("repository"),
		PageToken:  q.Get("page_token"),
	}
	data := eventsData{Page: "events", Query: query}

	listing, err := s.ListEvents(r.Context(), query)
	code := http.StatusOK
	if err != nil {
		code, data.Problem = runtime.HTTPStatusFromCode(status.Code(err)), eventProblem(status.Code(err))
	} else {
		data.Listing = listing
		data.NextURL = eventsURL(query, listing.GetNextPageToken())
	}

	render(w, r, code, s.views.events, data)
}

// eventsURL links the next page with the same filters, or "" at the end.
func eventsURL(q *pb.ListEventsRequest, token string) string {
	if token == "" {
		return ""
	}

	v := url.Values{"page_token": {token}}
	for key, value := range map[string]string{"source": q.GetSource(), "type": q.GetType(), "repository": q.GetRepository()} {
		if value != "" {
			v.Set(key, value)
		}
	}

	return "/events?" + v.Encode()
}

func (s *Server) event(w http.ResponseWriter, r *http.Request) {
	got, err := s.GetEvent(r.Context(), &pb.GetEventRequest{Id: r.PathValue("id")})
	if err != nil {
		code := status.Code(err)
		render(w, r, runtime.HTTPStatusFromCode(code), s.views.event, eventData{Page: "events", Problem: eventProblem(code)})

		return
	}

	e := got.GetEvent()
	render(w, r, http.StatusOK, s.views.event, eventData{
		Page:       "events",
		Event:      e,
		Deliveries: got.GetDeliveries(),
		Data:       prettyJSON(e.GetData()),
		Payload:    prettyJSON(e.GetNative().GetPayload()),
	})
}

// prettyJSON indents a Struct for display, or returns "" for none.
func prettyJSON(v *structpb.Struct) string {
	if v == nil {
		return ""
	}
	// protojson varies its whitespace, so indent with encoding/json.
	b, err := protojson.Marshal(v)
	var out bytes.Buffer
	if err != nil || json.Indent(&out, b, "", "  ") != nil {
		return ""
	}

	return out.String()
}

// lastEvents returns each source's newest event time, formatted. Sources
// without events, or whose lookup failed, are left out.
func (s *Server) lastEvents(ctx context.Context, sources []*pb.Webhook) map[string]string {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	out := map[string]string{}
	for _, src := range sources {
		e, ok, err := s.client.LatestEvent(ctx, src.GetId())
		if err != nil || !ok {
			continue
		}
		out[src.GetId()] = e.AcceptedAt.In(time.UTC).Format(timeLayout)
	}

	return out
}
