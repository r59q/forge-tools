package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-symbols/library/navigation"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
	pb "github.com/r59q/forge-tools/forge-symbols/service/symbolspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const token = "test-token"

func server(t *testing.T) *Server {
	t.Helper()
	analyzer := symbols.New(symbols.Limits{})
	t.Cleanup(func() { _ = analyzer.Close(context.Background()) })

	s, err := New(analyzer, forgeauth.StaticToken(token), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

// transport calls one RPC by method name, returning the status code.
type transport struct {
	name string
	call func(t *testing.T, method string, req, resp proto.Message) codes.Code
}

// transports serves s over in-process gRPC, with GRPCOptions, and over its
// REST handler.
func transports(t *testing.T, s *Server, authorization string) []transport {
	t.Helper()

	return []transport{
		{name: "gRPC", call: grpcCaller(t, s, authorization)},
		{name: "REST", call: restCaller(t, s, authorization)},
	}
}

func grpcCaller(t *testing.T, s *Server, authorization string) func(*testing.T, string, proto.Message, proto.Message) codes.Code {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	g := grpc.NewServer(s.GRPCOptions()...)
	pb.RegisterSymbolsServiceServer(g, s)
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

	return func(t *testing.T, method string, req, resp proto.Message) codes.Code {
		t.Helper()
		ctx := context.Background()
		if authorization != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", authorization)
		}

		return status.Code(conn.Invoke(ctx, "/forge.symbols.v1.SymbolsService/"+method, req, resp))
	}
}

func restCaller(t *testing.T, s *Server, authorization string) func(*testing.T, string, proto.Message, proto.Message) codes.Code {
	t.Helper()
	handler, err := s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	return func(t *testing.T, method string, req, resp proto.Message) codes.Code {
		t.Helper()
		body, err := protojson.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}

		rec := rest(handler, "/v1/symbols/"+strings.ToLower(method), body, int64(len(body)), authorization)
		if rec.Code != http.StatusOK {
			return restCode(t, rec)
		}

		if err := protojson.Unmarshal(rec.Body.Bytes(), resp); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}

		return codes.OK
	}
}

func rest(handler http.Handler, path string, body []byte, length int64, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.ContentLength = length
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

// restCode checks that an error response carries only a status code, with
// the HTTP status of that code.
func restCode(t *testing.T, rec *httptest.ResponseRecorder) codes.Code {
	t.Helper()
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", rec.Header())
	}

	var e struct {
		Code    codes.Code `json:"code"`
		Message string     `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body %q: %v", rec.Body, err)
	}

	if e.Message != e.Code.String() {
		t.Fatalf("error message %q echoes more than the code %v", e.Message, e.Code)
	}

	if want := httpStatus(e.Code); rec.Code != want {
		t.Fatalf("HTTP %d for %v, want %d", rec.Code, e.Code, want)
	}

	return e.Code
}

func httpStatus(code codes.Code) int {
	return map[codes.Code]int{
		codes.Unauthenticated:   http.StatusUnauthorized,
		codes.InvalidArgument:   http.StatusBadRequest,
		codes.NotFound:          http.StatusNotFound,
		codes.ResourceExhausted: http.StatusTooManyRequests,
		codes.Unimplemented:     http.StatusNotImplemented,
		codes.DeadlineExceeded:  http.StatusGatewayTimeout,
	}[code]
}

func TestNew(t *testing.T) {
	analyzer := symbols.New(symbols.Limits{})
	t.Cleanup(func() { _ = analyzer.Close(context.Background()) })

	for name, build := range map[string]func() (*Server, error){
		"no analyzer": func() (*Server, error) { return New(nil, forgeauth.StaticToken(token), time.Second) },
		"no token":    func() (*Server, error) { return New(analyzer, nil, time.Second) },
		"no timeout":  func() (*Server, error) { return New(analyzer, forgeauth.StaticToken(token), 0) },
	} {
		if _, err := build(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAuthentication(t *testing.T) {
	s := server(t)
	for _, authorization := range []string{"", "Bearer wrong", "Basic " + token} {
		for _, tr := range transports(t, s, authorization) {
			got := tr.call(t, "Analyze", &pb.AnalyzeRequest{Files: sample()}, &pb.AnalyzeResponse{})
			if got != codes.Unauthenticated {
				t.Errorf("%s %q: %v", tr.name, authorization, got)
			}
		}
	}
}

// TestRESTChecksBeforeReading sends a Content-Length over the limit: the
// token is checked first, then the length, before the body is read.
func TestRESTChecksBeforeReading(t *testing.T) {
	s := server(t)
	handler, err := s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if rec := rest(handler, "/v1/symbols/analyze", []byte("{}"), s.BodyBytes()+1, ""); restCode(t, rec) != codes.Unauthenticated {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}

	if rec := rest(handler, "/v1/symbols/analyze", []byte("{}"), s.BodyBytes()+1, "Bearer "+token); restCode(t, rec) != codes.ResourceExhausted {
		t.Fatalf("too long: %d", rec.Code)
	}
}

func TestLimits(t *testing.T) {
	s := server(t)
	limits := symbols.DefaultLimits()
	if s.MessageBytes() <= limits.TotalBytes || s.BodyBytes() <= int64(limits.TotalBytes*4/3) {
		t.Fatalf("limits %d %d do not fit %d content bytes", s.MessageBytes(), s.BodyBytes(), limits.TotalBytes)
	}
}

func TestErrors(t *testing.T) {
	s := server(t)
	cases := []struct {
		name   string
		method string
		req    proto.Message
		want   codes.Code
	}{
		{"unsupported language", "Analyze", &pb.AnalyzeRequest{Files: []*pb.File{{Path: "notes.txt", Content: []byte("x")}}}, codes.Unimplemented},
		{"unknown language", "Analyze", &pb.AnalyzeRequest{Files: []*pb.File{{Path: "a.go", Language: "cobol"}}}, codes.Unimplemented},
		{"oversize file", "Analyze", &pb.AnalyzeRequest{Files: []*pb.File{{Path: "big.go", Content: bytes.Repeat([]byte("\n"), 1<<20+1)}}},
			codes.ResourceExhausted},
		{"too many files", "Analyze", &pb.AnalyzeRequest{Files: make([]*pb.File, 1001)}, codes.ResourceExhausted},
		{"no target", "Outgoing", &pb.OutgoingRequest{Files: sample()}, codes.InvalidArgument},
		{"position without path", "Incoming", &pb.IncomingRequest{Files: sample(), Target: &pb.Target{Line: 1, Column: 1}},
			codes.InvalidArgument},
		{"negative max_results", "Definitions", &pb.DefinitionsRequest{Files: sample(), Target: &pb.Target{Name: "Greet"}, MaxResults: -1},
			codes.InvalidArgument},
		{"max_results over 1000", "Definitions", &pb.DefinitionsRequest{Files: sample(), Target: &pb.Target{Name: "Greet"}, MaxResults: 1001},
			codes.InvalidArgument},
		{"position on nothing", "Outgoing", &pb.OutgoingRequest{Files: sample(), Target: &pb.Target{Path: "callee.go", Line: 2, Column: 1}},
			codes.NotFound},
	}

	for _, tr := range transports(t, s, "Bearer "+token) {
		for _, c := range cases {
			resp := c.req.ProtoReflect().Type().New().Interface()
			if got := tr.call(t, c.method, c.req, resp); got != c.want {
				t.Errorf("%s %s: got %v, want %v", tr.name, c.name, got, c.want)
			}
		}
	}
}

func TestTimeout(t *testing.T) {
	analyzer := symbols.New(symbols.Limits{})
	t.Cleanup(func() { _ = analyzer.Close(context.Background()) })

	s, err := New(analyzer, forgeauth.StaticToken(token), time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	if _, err := s.Analyze(ctx, &pb.AnalyzeRequest{Files: sample()}); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
}

func TestCode(t *testing.T) {
	for err, want := range map[error]codes.Code{
		context.Canceled:                                 codes.Canceled,
		context.DeadlineExceeded:                         codes.DeadlineExceeded,
		symbols.ErrUnsupported:                           codes.Unimplemented,
		fmt.Errorf("parse: %w", symbols.ErrTooLarge):     codes.ResourceExhausted,
		symbols.ErrMemory:                                codes.ResourceExhausted,
		symbols.ErrTimeout:                               codes.DeadlineExceeded,
		navigation.ErrInvalidTarget:                      codes.InvalidArgument,
		errBadRequest:                                    codes.InvalidArgument,
		fmt.Errorf("target: %w", navigation.ErrNotFound): codes.NotFound,
		errors.New("other"):                              codes.Internal,
	} {
		if got := code(err); got != want {
			t.Errorf("%v: got %v, want %v", err, got, want)
		}
	}
}
