package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	pb "github.com/r59q/forge-tools/forge-agents/service/agentspb"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

// writeSlack is how long past the stream timeout a stream may take to
// write its last event.
const writeSlack = 5 * time.Second

// errSend marks a failure to deliver an event, which ends the stream with
// the transport's own error.
var errSend = errors.New("api: event not delivered")

// StreamEvents sends the events after req.After, then each new event,
// until the agent ends, is deleted or the stream timeout passes.
func (s *Server) StreamEvents(req *pb.StreamEventsRequest, stream grpc.ServerStreamingServer[pb.StreamEventsResponse]) error {
	ctx := stream.Context()
	if err := servicekit.Authorize(ctx, s.config.Token); err != nil {
		return err
	}
	var sendErr error
	err := s.watch(ctx, req.GetId(), req.GetAfter(), func(e *pb.Event) error {
		if sendErr = stream.Send(&pb.StreamEventsResponse{Event: e}); sendErr != nil {
			return errSend
		}
		return nil
	})
	if errors.Is(err, errSend) {
		return sendErr
	}
	return err
}

// watch runs the manager's watch within the stream timeout. Reaching the
// timeout ends the stream normally; callers resume from the last event.
// Other errors are bare status codes, or errSend.
func (s *Server) watch(ctx context.Context, id string, after int64, send func(*pb.Event) error) error {
	stream, cancel := context.WithTimeout(ctx, s.config.StreamTimeout)
	defer cancel()
	err := s.config.Manager.Watch(stream, id, after, func(e transcript.Event) error { return send(eventPB(e)) })
	switch {
	case err == nil, errors.Is(err, errSend):
		return err
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		return nil
	}
	return servicekit.Status(code(err))
}

// stream serves GET /v1/agents/{id}/stream?after=N: one JSON
// StreamEventsResponse per line, flushed as each event is recorded. Errors
// before the response starts use fail; later errors abort the connection
// so the cut shows, while a normal end closes the body.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, fail func(http.ResponseWriter, *http.Request, error)) {
	after, err := parseAfter(r.URL.RawQuery)
	if err != nil {
		fail(w, r, servicekit.Status(code(err)))
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), s.config.Timeout)
	_, err = s.config.Manager.Get(ctx, id)
	cancel()
	if err != nil {
		fail(w, r, servicekit.Status(code(err)))
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(s.config.StreamTimeout + writeSlack))
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	marshal := protojson.MarshalOptions{EmitUnpopulated: true}
	err = s.watch(r.Context(), id, after, func(e *pb.Event) error {
		line, err := marshal.Marshal(&pb.StreamEventsResponse{Event: e})
		if err != nil {
			return errSend
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return errSend
		}
		if rc.Flush() != nil {
			return errSend
		}
		return nil
	})
	if err != nil {
		panic(http.ErrAbortHandler)
	}
}

// parseAfter parses the stream query: after, at most once, a sequence
// number that is not negative.
func parseAfter(query string) (int64, error) {
	bad := fmt.Errorf("%w: stream query", errBadRequest)
	q, err := url.ParseQuery(query)
	if err != nil {
		return 0, bad
	}
	var after int64
	for key, values := range q {
		if key != "after" || len(values) != 1 {
			return 0, bad
		}
		after, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || after < 0 {
			return 0, bad
		}
	}
	return after, nil
}
