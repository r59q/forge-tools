package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/adapter/codex"
	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/executor"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
	pb "github.com/r59q/forge-tools/forge-agents/service/agentspb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const token = "service-token"

// script is a hand-built Codex turn: no model is ever called.
const script = `{"type":"thread.started","thread_id":"thread-1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"hello"}}
{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":5}}
`

// fakeExecutor replays script for every turn. With hold set, it stops
// after the first line until the turn is cancelled.
type fakeExecutor struct {
	hold bool
}

func (f fakeExecutor) Run(ctx context.Context, _ executor.Command, stdout func(io.Reader)) (executor.Exit, error) {
	r, w := io.Pipe()
	go func() {
		out := script
		if f.hold {
			out, _, _ = strings.Cut(script, "\n")
			out += "\n"
		}
		_, _ = io.WriteString(w, out)
		if f.hold {
			<-ctx.Done()
		}
		_ = w.Close()
	}()
	stdout(r)
	if ctx.Err() != nil {
		return executor.Exit{Code: -1}, ctx.Err()
	}
	return executor.Exit{}, nil
}

// newServer returns a server whose manager runs fake Codex turns in
// scratch workspaces.
func newServer(t *testing.T, exec executor.Executor) *Server {
	t.Helper()
	m, err := agent.Open(t.Context(), agent.Config{
		Store:      agent.NewMemoryStore(),
		Clients:    map[string]agent.Client{"codex": {Adapter: codex.Adapter{}, Path: "/usr/bin/codex"}},
		Executor:   exec,
		Workspaces: workspace.Local{ScratchDir: t.TempDir()},
		RunDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s, err := New(Config{Manager: m, Token: forgeauth.StaticToken(token), Timeout: 10 * time.Second, StreamTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// dial serves s over an in-memory gRPC connection.
func dial(t *testing.T, s *Server) pb.AgentsServiceClient {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	g := grpc.NewServer(s.GRPCOptions()...)
	pb.RegisterAgentsServiceServer(g, s)
	go func() { _ = g.Serve(listener) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewAgentsServiceClient(conn)
}

// authorized returns a context carrying authorization, if any.
func authorized(t *testing.T, authorization string) context.Context {
	t.Helper()
	if authorization == "" {
		return t.Context()
	}
	return metadata.AppendToOutgoingContext(t.Context(), "authorization", authorization)
}

// restServer serves the REST routes of s over HTTP.
func restServer(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	handler, err := s.REST(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// do sends a REST request and returns the status and body.
func do(t *testing.T, server *httptest.Server, method, path string, body proto.Message, authorization string) (int, []byte) {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		if data, err = protojson.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("%s %s headers: %v", method, path, resp.Header)
	}
	return resp.StatusCode, out
}

// restCode returns the code of a REST error, checking that the error
// carries nothing but its code.
func restCode(t *testing.T, body []byte) codes.Code {
	t.Helper()
	var e struct {
		Code    codes.Code `json:"code"`
		Message string     `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body %q: %v", body, err)
	}
	if !bare(e.Code, e.Message) {
		t.Fatalf("error message %q echoes more than the code %v", e.Message, e.Code)
	}
	return e.Code
}

// streamREST reads the NDJSON stream of an agent until it ends.
func streamREST(t *testing.T, server *httptest.Server, id string, after int64) []*pb.Event {
	t.Helper()
	path := server.URL + "/v1/agents/" + url.PathEscape(id) + "/stream?after=" + strconv.FormatInt(after, 10)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("stream: %d %v", resp.StatusCode, resp.Header)
	}
	var events []*pb.Event
	lines := bufio.NewScanner(resp.Body)
	lines.Buffer(nil, 1<<20)
	for lines.Scan() {
		var r pb.StreamEventsResponse
		if err := protojson.Unmarshal(lines.Bytes(), &r); err != nil {
			t.Fatalf("stream line %q: %v", lines.Bytes(), err)
		}
		events = append(events, r.GetEvent())
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

// grpcCode checks that a gRPC error carries nothing but its code.
func grpcCode(t *testing.T, err error) codes.Code {
	t.Helper()
	if s := status.Convert(err); err != nil && !bare(s.Code(), s.Message()) {
		t.Fatalf("error message %q echoes more than the code", s.Message())
	}
	return status.Code(err)
}

// waitState polls an agent until it reaches state.
func waitState(t *testing.T, client pb.AgentsServiceClient, id string, state agent.State) *pb.Agent {
	t.Helper()
	ctx := authorized(t, "Bearer "+token)
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := client.GetAgent(ctx, &pb.GetAgentRequest{Id: id})
		if err != nil {
			t.Fatal(err)
		}
		if r.GetAgent().GetState() == string(state) {
			return r.GetAgent()
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent is %s, want %s", r.GetAgent().GetState(), state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// bare reports whether message says nothing beyond code: its name, or
// servicekit's fixed authentication message.
func bare(code codes.Code, message string) bool {
	return message == code.String() || code == codes.Unauthenticated && message == "authentication required"
}
