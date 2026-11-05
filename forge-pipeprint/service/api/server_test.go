package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	pb "github.com/r59q/forge-tools/forge-pipeprint/service/pipeprintpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const token = "test-token"

const workflow = `name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make
  test:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - run: make test
`

func server(t *testing.T) *Server {
	t.Helper()
	s, err := New(forgeauth.StaticToken(token), time.Second)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func authed() context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

func TestNew(t *testing.T) {
	if _, err := New(nil, time.Second); err == nil {
		t.Fatal("accepted no token")
	}

	if _, err := New(forgeauth.StaticToken(token), 0); err == nil {
		t.Fatal("accepted no timeout")
	}
}

func TestAuthentication(t *testing.T) {
	_, err := server(t).ListFormats(context.Background(), &pb.ListFormatsRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v", err)
	}
}

func TestConvert(t *testing.T) {
	resp, err := server(t).Convert(authed(), &pb.ConvertRequest{Content: workflow, To: "gitlab"})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(resp.GetContent(), "script:\n    - make test\n") || len(resp.GetWarnings()) != 2 {
		t.Fatalf("got %s %v", resp.GetContent(), resp.GetWarnings())
	}

	for _, r := range []*pb.ConvertRequest{
		{Content: workflow, To: "jenkins"},
		{Content: workflow, From: "jenkins", To: "gitlab"},
		{Content: "a: [", To: "gitlab"},
	} {
		_, err := server(t).Convert(authed(), r)
		if status.Code(err) != codes.InvalidArgument || strings.Contains(err.Error(), "jenkins") {
			t.Errorf("%v: %v", r, err)
		}
	}
}

func TestCheck(t *testing.T) {
	s := server(t)
	resp, err := s.Check(authed(), &pb.CheckRequest{Content: workflow})
	if err != nil || !resp.GetValid() || resp.GetFormat() != "github" {
		t.Fatalf("valid: %v %v", resp, err)
	}

	resp, err = s.Check(authed(), &pb.CheckRequest{Content: "pipeprint: v1\njobs: [{id: a, needs: [b]}]"})
	if err != nil || resp.GetValid() || resp.GetProblems()[0].GetPath() != "jobs.a.needs" {
		t.Fatalf("invalid: %v %v", resp, err)
	}

	if _, err := s.Check(authed(), &pb.CheckRequest{From: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("format: %v", err)
	}
}

func TestGraphAndRender(t *testing.T) {
	s := server(t)
	g, err := s.Graph(authed(), &pb.GraphRequest{Content: workflow, From: "forgejo"})
	if err != nil {
		t.Fatal(err)
	}

	if g.GetLevels() != 2 || len(g.GetNodes()) != 2 || g.GetEdges()[0].GetFrom() != "build" || g.GetTriggers() != "push" ||
		g.GetNodes()[0].GetSteps()[1] != "$ make" {
		t.Fatalf("graph %v", g)
	}

	text, err := s.Render(authed(), &pb.RenderRequest{Content: workflow, Ascii: true, Steps: true})
	if err != nil || !strings.Contains(text.GetText(), "| <- build") {
		t.Fatalf("render %v %v", text, err)
	}

	for _, width := range []int32{-1, maxWidth + 1} {
		if _, err := s.Render(authed(), &pb.RenderRequest{Content: workflow, Width: width}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("width %d: %v", width, err)
		}
	}

	if _, err := s.Graph(authed(), &pb.GraphRequest{Content: "["}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad content: %v", err)
	}
}

func TestREST(t *testing.T) {
	handler, err := server(t).REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"content": workflow, "to": "pipeprint"})
	req := httptest.NewRequest(http.MethodPost, "/v1/pipeprint/convert", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var resp struct{ Content string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil || !strings.HasPrefix(resp.Content, "pipeprint: v1") {
		t.Fatalf("convert: %d %s", rec.Code, rec.Body)
	}

	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("response may be cached")
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/pipeprint/formats", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated formats: %d", rec.Code)
	}
}

func TestCode(t *testing.T) {
	if code(context.Canceled) != codes.Canceled || code(errBadRequest) != codes.InvalidArgument || code(ErrInvalid) != codes.Internal {
		t.Fatal("code mapping")
	}
}
