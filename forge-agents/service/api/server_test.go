package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	pb "github.com/r59q/forge-tools/forge-agents/service/agentspb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestNew(t *testing.T) {
	m := newServer(t, fakeExecutor{}).config.Manager
	valid := Config{Manager: m, Token: forgeauth.StaticToken(token), Timeout: time.Second, StreamTimeout: time.Second}
	if _, err := New(valid); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"no manager":        func(c *Config) { c.Manager = nil },
		"no token":          func(c *Config) { c.Token = nil },
		"no timeout":        func(c *Config) { c.Timeout = 0 },
		"no stream timeout": func(c *Config) { c.StreamTimeout = 0 },
	} {
		config := valid
		change(&config)
		if _, err := New(config); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestAuthentication checks every RPC and the REST stream with missing and
// wrong tokens.
func TestAuthentication(t *testing.T) {
	s := newServer(t, fakeExecutor{})
	client, server := dial(t, s), restServer(t, s)
	for _, authorization := range []string{"", "Bearer wrong", "Basic " + token, "Bearer " + token + "x"} {
		ctx := authorized(t, authorization)
		calls := map[string]func() error{
			"create": func() error { _, err := client.CreateAgent(ctx, &pb.CreateAgentRequest{Client: "codex"}); return err },
			"get":    func() error { _, err := client.GetAgent(ctx, &pb.GetAgentRequest{Id: "a"}); return err },
			"list":   func() error { _, err := client.ListAgents(ctx, &pb.ListAgentsRequest{}); return err },
			"send":   func() error { _, err := client.SendMessage(ctx, &pb.SendMessageRequest{Id: "a"}); return err },
			"answer": func() error { _, err := client.AnswerInput(ctx, &pb.AnswerInputRequest{Id: "a"}); return err },
			"cancel": func() error { _, err := client.CancelTurn(ctx, &pb.CancelTurnRequest{Id: "a"}); return err },
			"stop":   func() error { _, err := client.StopAgent(ctx, &pb.StopAgentRequest{Id: "a"}); return err },
			"delete": func() error { _, err := client.DeleteAgent(ctx, &pb.DeleteAgentRequest{Id: "a"}); return err },
			"events": func() error { _, err := client.ListEvents(ctx, &pb.ListEventsRequest{Id: "a"}); return err },
			"output": func() error { _, err := client.GetTurnOutput(ctx, &pb.GetTurnOutputRequest{Id: "a"}); return err },
			"stream": func() error {
				stream, err := client.StreamEvents(ctx, &pb.StreamEventsRequest{Id: "a"})
				if err != nil {
					return err
				}
				_, err = stream.Recv()
				return err
			},
		}
		for name, call := range calls {
			if got := grpcCode(t, call()); got != codes.Unauthenticated {
				t.Errorf("gRPC %s %q: %v", name, authorization, got)
			}
		}
		for _, path := range []string{"/v1/agents", "/v1/agents/a", "/v1/agents/a/events", "/v1/agents/a/stream"} {
			status, body := do(t, server, http.MethodGet, path, nil, authorization)
			if status != http.StatusUnauthorized || restCode(t, body) != codes.Unauthenticated {
				t.Errorf("REST %s %q: %d %s", path, authorization, status, body)
			}
		}
	}
	list, err := client.ListAgents(authorized(t, "Bearer "+token), &pb.ListAgentsRequest{})
	if err != nil || len(list.GetAgents()) != 0 {
		t.Fatalf("rejected calls created agents: %v, %v", list, err)
	}
}

// TestGRPC runs an agent through a turn over gRPC.
func TestGRPC(t *testing.T) {
	client := dial(t, newServer(t, fakeExecutor{}))
	ctx := authorized(t, "Bearer "+token)
	created, err := client.CreateAgent(ctx, &pb.CreateAgentRequest{
		Client: "codex", Policy: &pb.Policy{Mode: "edit"}, Limits: &pb.Limits{TurnTimeout: durationpb.New(time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := created.GetAgent()
	if a.GetState() != "created" || a.GetWorkspace().GetKind() != "scratch" || a.GetLimits().GetTurnTimeout().AsDuration() != time.Minute {
		t.Fatalf("created %v", a)
	}
	if _, err := client.SendMessage(ctx, &pb.SendMessageRequest{Id: a.GetId(), Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.StreamEvents(ctx, &pb.StreamEventsRequest{Id: a.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for {
		r, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, r.GetEvent().GetKind())
		if r.GetEvent().GetKind() == string(transcript.KindResult) {
			break
		}
	}
	if got := strings.Join(kinds, " "); got != "message message usage result" {
		t.Errorf("streamed %s", got)
	}

	idle := waitState(t, client, a.GetId(), agent.StateIdle)
	if idle.GetSession() != "thread-1" || idle.GetTurns() != 1 {
		t.Errorf("idle agent %v", idle)
	}
	page, err := client.ListEvents(ctx, &pb.ListEventsRequest{Id: a.GetId(), After: 1, Limit: 2})
	if err != nil || len(page.GetEvents()) != 2 || page.GetEvents()[0].GetText() != "hello" || page.GetEvents()[1].GetUsage().GetInputTokens() != 10 {
		t.Errorf("page %v, %v", page, err)
	}
	output, err := client.GetTurnOutput(ctx, &pb.GetTurnOutputRequest{Id: a.GetId(), Turn: 1})
	if err != nil || !strings.Contains(string(output.GetOutput().GetStdout()), "thread.started") {
		t.Errorf("output %v, %v", output, err)
	}
	list, err := client.ListAgents(ctx, &pb.ListAgentsRequest{State: "idle"})
	if err != nil || len(list.GetAgents()) != 1 {
		t.Errorf("idle agents %v, %v", list, err)
	}

	stopped, err := client.StopAgent(ctx, &pb.StopAgentRequest{Id: a.GetId()})
	if err != nil || stopped.GetAgent().GetState() != "stopped" {
		t.Fatalf("stop %v, %v", stopped, err)
	}
	// The stream of a stopped agent ends once every event was sent.
	ended, err := client.StreamEvents(ctx, &pb.StreamEventsRequest{Id: a.GetId(), After: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ended.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("stream of a stopped agent: %v", err)
	}
	if _, err := client.DeleteAgent(ctx, &pb.DeleteAgentRequest{Id: a.GetId()}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetAgent(ctx, &pb.GetAgentRequest{Id: a.GetId()}); grpcCode(t, err) != codes.NotFound {
		t.Errorf("get deleted: %v", err)
	}
}

// TestREST runs an agent through a turn over REST and the NDJSON stream.
func TestREST(t *testing.T) {
	server := restServer(t, newServer(t, fakeExecutor{}))
	auth := "Bearer " + token
	status, body := do(t, server, http.MethodPost, "/v1/agents", &pb.CreateAgentRequest{Client: "codex"}, auth)
	if status != http.StatusOK {
		t.Fatalf("create: %d %s", status, body)
	}
	var a pb.Agent
	unmarshal(t, body, &a)
	path := "/v1/agents/" + a.GetId()
	if status, body := do(t, server, http.MethodPost, path+"/messages", &pb.SendMessageRequest{Text: "hi"}, auth); status != http.StatusOK {
		t.Fatalf("send: %d %s", status, body)
	}
	waitREST(t, server, path, "idle")
	if status, body := do(t, server, http.MethodPost, path+"/stop", &pb.StopAgentRequest{}, auth); status != http.StatusOK {
		t.Fatalf("stop: %d %s", status, body)
	}

	events := streamREST(t, server, a.GetId(), 0)
	if len(events) != 4 || events[0].GetRole() != "user" || events[0].GetText() != "hi" || events[3].GetStatus() != "success" {
		t.Errorf("streamed %v", events)
	}
	if events := streamREST(t, server, a.GetId(), 3); len(events) != 1 || events[0].GetSeq() != 4 {
		t.Errorf("streamed after 3: %v", events)
	}
	status, body = do(t, server, http.MethodGet, path+"/events?after=2", nil, auth)
	var page pb.ListEventsResponse
	unmarshal(t, body, &page)
	if status != http.StatusOK || len(page.GetEvents()) != 2 {
		t.Errorf("events: %d %s", status, body)
	}
	status, body = do(t, server, http.MethodGet, path+"/turns/1/output", nil, auth)
	var output pb.TurnOutput
	unmarshal(t, body, &output)
	if status != http.StatusOK || !strings.Contains(string(output.GetStdout()), "turn.completed") {
		t.Errorf("output: %d %s", status, body)
	}
	if status, body := do(t, server, http.MethodDelete, path, nil, auth); status != http.StatusOK {
		t.Fatalf("delete: %d %s", status, body)
	}
}

// TestCancel kills a running turn and returns the agent to idle.
func TestCancel(t *testing.T) {
	client := dial(t, newServer(t, fakeExecutor{hold: true}))
	ctx := authorized(t, "Bearer "+token)
	created, err := client.CreateAgent(ctx, &pb.CreateAgentRequest{Client: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetAgent().GetId()
	if _, err := client.SendMessage(ctx, &pb.SendMessageRequest{Id: id, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendMessage(ctx, &pb.SendMessageRequest{Id: id, Text: "again"}); grpcCode(t, err) != codes.FailedPrecondition {
		t.Errorf("send while running: %v", err)
	}
	cancelled, err := client.CancelTurn(ctx, &pb.CancelTurnRequest{Id: id})
	if err != nil || cancelled.GetAgent().GetState() != "idle" {
		t.Fatalf("cancel %v, %v", cancelled, err)
	}
	page, err := client.ListEvents(ctx, &pb.ListEventsRequest{Id: id})
	if err != nil || len(page.GetEvents()) == 0 {
		t.Fatalf("events %v, %v", page, err)
	}
	if last := page.GetEvents()[len(page.GetEvents())-1]; last.GetKind() != "result" || last.GetStatus() != "cancelled" {
		t.Errorf("last event %v", last)
	}
}

// TestErrors checks that failures come back as bare status codes.
func TestErrors(t *testing.T) {
	s := newServer(t, fakeExecutor{})
	client, server := dial(t, s), restServer(t, s)
	ctx := authorized(t, "Bearer "+token)
	for name, c := range map[string]struct {
		call func() error
		want codes.Code
	}{
		"unknown agent": {func() error { _, err := client.GetAgent(ctx, &pb.GetAgentRequest{Id: "nope"}); return err }, codes.NotFound},
		"unknown client": {func() error {
			_, err := client.CreateAgent(ctx, &pb.CreateAgentRequest{Client: "other"})
			return err
		}, codes.InvalidArgument},
		"ask without pausing": {func() error {
			_, err := client.CreateAgent(ctx, &pb.CreateAgentRequest{Client: "codex", Policy: &pb.Policy{Ask: true}})
			return err
		}, codes.InvalidArgument},
		"negative turn timeout": {func() error {
			limits := &pb.Limits{TurnTimeout: durationpb.New(-time.Second)}
			_, err := client.CreateAgent(ctx, &pb.CreateAgentRequest{Client: "codex", Limits: limits})
			return err
		}, codes.InvalidArgument},
		"checkouts not configured": {func() error {
			w := &pb.WorkspaceSpec{Kind: "checkout", Repository: &pb.Repository{Source: "github", Path: "o/r"}}
			_, err := client.CreateAgent(ctx, &pb.CreateAgentRequest{Client: "codex", Workspace: w})
			return err
		}, codes.FailedPrecondition},
		"negative page": {func() error {
			_, err := client.ListEvents(ctx, &pb.ListEventsRequest{Id: "nope", After: -1})
			return err
		}, codes.InvalidArgument},
		"stream of unknown agent": {func() error {
			stream, err := client.StreamEvents(ctx, &pb.StreamEventsRequest{Id: "nope"})
			if err == nil {
				_, err = stream.Recv()
			}
			return err
		}, codes.NotFound},
	} {
		if got := grpcCode(t, c.call()); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	for path, want := range map[string]codes.Code{
		"/v1/agents/nope/stream":             codes.NotFound,
		"/v1/agents/nope/stream?after=-1":    codes.InvalidArgument,
		"/v1/agents/nope/stream?after=1&x=2": codes.InvalidArgument,
		"/v1/agents/nope/turns/1/output":     codes.NotFound,
	} {
		if _, body := do(t, server, http.MethodGet, path, nil, "Bearer "+token); restCode(t, body) != want {
			t.Errorf("%s: %s, want %v", path, body, want)
		}
	}
	big := strings.Repeat("x", RequestBytes+1)
	if status, body := do(t, server, http.MethodPost, "/v1/agents/a/messages", &pb.SendMessageRequest{Text: big}, "Bearer "+token); status != http.StatusTooManyRequests {
		t.Errorf("oversized body: %d %s", status, body)
	}
}

func TestCode(t *testing.T) {
	for err, want := range map[error]codes.Code{
		agent.ErrTranscriptFull:  codes.ResourceExhausted,
		agent.ErrLimit:           codes.ResourceExhausted,
		ErrCheckoutTooLarge:      codes.ResourceExhausted,
		ErrCheckoutConflict:      codes.FailedPrecondition,
		ErrCheckoutNotFound:      codes.NotFound,
		ErrCheckoutsUnavailable:  codes.Unavailable,
		errors.New("unexpected"): codes.Internal,
	} {
		if got := code(err); got != want {
			t.Errorf("%v: %v, want %v", err, got, want)
		}
	}
}

func unmarshal(t *testing.T, body []byte, m proto.Message) {
	t.Helper()
	if err := protojson.Unmarshal(body, m); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
}

// waitREST polls an agent over REST until it reaches state.
func waitREST(t *testing.T, server *httptest.Server, path, state string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, body := do(t, server, http.MethodGet, path, nil, "Bearer "+token)
		var a pb.Agent
		unmarshal(t, body, &a)
		if status == http.StatusOK && a.GetState() == state {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent: %d %s, want %s", status, body, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
