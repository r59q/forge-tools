package main

import (
	"context"
	"io"
	"math"
	"net"
	"strings"
	"testing"

	pb "github.com/r59q/forge-tools/forge-actions/service/actionspb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type pipelineBackend struct {
	pb.UnimplementedActionsServiceServer
	t *testing.T
}

func (b *pipelineBackend) ListPipelines(ctx context.Context, r *pb.ListPipelinesRequest) (*pb.ListPipelinesResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if a := md.Get("authorization"); len(a) != 1 || a[0] != "Bearer actions-synthetic" {
		return nil, status.Error(codes.Unauthenticated, "backend-private-detail")
	}
	if len(md.Get("private-browser-header")) > 0 {
		b.t.Error("forwarded browser metadata")
	}

	return &pb.ListPipelinesResponse{Pipelines: []*pb.Pipeline{{
		Ref: &pb.Ref{
			Source:     r.Source,
			Repository: r.Repository,
			Id:         "7",
		},
		Name:   "Build",
		Status: &pb.Status{Phase: "running"},
	}}}, nil
}

func TestRunWithPipelines(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	g := grpc.NewServer()
	pb.RegisterActionsServiceServer(g, &pipelineBackend{t: t})
	go func() { _ = g.Serve(l) }()
	defer g.Stop()
	t.Setenv("MANAGEMENT_ACTIONS_TEST_TOKEN", "actions-synthetic")
	s, err := parse(
		[]string{"--token-file=" + writeToken(t, "operator-synthetic"), "--http-listen=127.0.0.1:0", "--grpc-listen=127.0.0.1:0", "--actions-address=" + l.Addr().String(), "--actions-insecure=true", "--actions-token-env=MANAGEMENT_ACTIONS_TEST_TOKEN"},
		func(string) string { return "" },
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}

	a := startManagement(t, s)
	for _, web := range []bool{false, true} {
		path := "/v1/pipelines?source=source&repository=o/r"
		if web {
			path = "/pipelines?source=source&repository=o/r"
		}

		code, body := httpGet(t, a[1], path, web)
		if code != 200 || !strings.Contains(body, "Build") {
			t.Fatalf("%d %s", code, body)
		}
	}

	t.Setenv("MANAGEMENT_ACTIONS_TEST_TOKEN", "operator-synthetic")
	code, body := httpGet(t, a[1], "/v1/pipelines?source=source&repository=o/r", false)
	if code != 503 || strings.Contains(body, "synthetic") {
		t.Fatalf("credential rotation %d %s", code, body)
	}
}

func TestActionsSettings(t *testing.T) {
	for _, args := range [][]string{
		{"--actions-insecure=true"},
		{"--actions-token-env=TOKEN"},
		{"--actions-address=localhost:9099", "--actions-token-file=f", "--actions-token-env=TOKEN"},
		{"--actions-address=localhost:9099", "--actions-insecure=bad"},
		{"--actions-connector-token-env=TOKEN"},
	} {
		if _, err := parse(args, func(string) string { return "" }, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}

	s, err := parse(
		[]string{"--actions-address=localhost:9099", "--connector-address=localhost:9090", "--actions-connector-token-env=TEST_CLIENT"},
		func(string) string { return "" },
		io.Discard,
	)
	if err != nil || s.actionsTokenEnv != "FORGE_MANAGEMENT_ACTIONS_TOKEN" {
		t.Fatalf("%+v %v", s, err)
	}

	found := false
	for _, c := range s.clients {
		if c.name == "forge-actions" {
			found = c.env == "TEST_CLIENT" && !c.optional
		}
	}

	if !found {
		t.Fatal("actions client not registered")
	}
}

func TestClamp32(t *testing.T) {
	for in, want := range map[int]int32{
		0:                 0,
		1000000:           1000000,
		-1:                -1,
		math.MaxInt32 + 1: math.MaxInt32,
		math.MinInt32 - 1: math.MinInt32,
	} {
		if got := clamp32(in); got != want {
			t.Errorf("clamp32(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestBackendsLeaveUnsetNil(t *testing.T) {
	t.Setenv("MANAGEMENT_TEST_TOKEN", "operator-synthetic")
	t.Setenv("MANAGEMENT_TEST_ACTIONS", "actions-synthetic")
	s := settings{actionsAddress: "localhost:1", actionsTokenEnv: "MANAGEMENT_TEST_ACTIONS"}
	config, closeAll, err := s.backends(context.Background(), forgeauth.EnvToken("MANAGEMENT_TEST_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}

	defer closeAll()
	if config.Repositories != nil || config.Issues != nil || config.Pipelines == nil {
		t.Fatalf("config = %+v", config)
	}
}
