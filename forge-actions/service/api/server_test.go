package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-actions/library/actions"
	pb "github.com/r59q/forge-tools/forge-actions/service/actionspb"
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

func apiFixture(t *testing.T) (*Server, http.Handler, pb.ActionsServiceClient) {
	t.Helper()
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/actions/runs":
			fmt.Fprint(w, `{"total_count":1,"workflow_runs":[{"id":7,"name":"CI","status":"completed","conclusion":"success"}]}`)
		case "/repos/o/r/actions/runs/7":
			fmt.Fprint(w, `{"id":7,"name":"CI","status":"completed","conclusion":"success"}`)
		case "/repos/o/r/actions/runs/7/jobs":
			fmt.Fprint(w, `{"jobs":[{"id":9,"run_id":7,"name":"test","status":"completed","conclusion":"success","steps":[]}]}`)
		case "/repos/o/r/actions/jobs/9":
			fmt.Fprint(w, `{"id":9,"run_id":7,"name":"test","status":"completed","conclusion":"success","steps":[]}`)
		case "/repos/o/r/actions/jobs/9/logs":
			fmt.Fprint(w, "hello\n")
		case "/repos/o/r/actions/runs/7/artifacts":
			fmt.Fprint(w, `{"artifacts":[{"id":11,"name":"build","expired":true}]}`)
		case "/repos/o/r/actions/artifacts/11/zip":
			fmt.Fprint(w, "archive")
		case "/repos/o/r/actions/workflows":
			fmt.Fprint(w, `{"workflows":[]}`)
		default:
			w.WriteHeader(403)
			fmt.Fprint(w, "upstream-secret-sentinel")
		}
	}))
	t.Cleanup(forge.Close)
	c, err := actions.New(actions.Source{ID: "source", Session: &forgeauth.Session{
		Kind:    forgeauth.GitHub,
		BaseURL: forge.URL,
		Client:  forge.Client(),
	}})
	if err != nil {
		t.Fatal(err)
	}

	s, err := New(Fixed(c), forgeauth.StaticToken("service"), time.Second)
	if err != nil {
		t.Fatal(err)
	}

	h, err := s.REST(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	l := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	pb.RegisterActionsServiceServer(g, s)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient(
		"passthrough:///test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return l.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return s, h, pb.NewActionsServiceClient(conn)
}

func TestRESTGRPCParity(t *testing.T) {
	_, h, c := apiFixture(t)
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer service"))
	for _, tc := range []struct {
		path  string
		call  func() (proto.Message, error)
		empty proto.Message
	}{
		{"sources", func() (proto.Message, error) { return c.ListSources(ctx, &pb.ListSourcesRequest{}) }, &pb.ListSourcesResponse{}},
		{"pipelines", func() (proto.Message, error) {
			return c.ListPipelines(ctx, &pb.ListPipelinesRequest{Source: "source", Repository: "o/r"})
		}, &pb.ListPipelinesResponse{}},
		{"pipelines/7", func() (proto.Message, error) {
			v, err := c.GetPipeline(ctx, &pb.GetPipelineRequest{Source: "source", Repository: "o/r", Id: "7"})
			return v.GetPipeline(), err
		}, &pb.Pipeline{}},
		{"pipelines/7/jobs", func() (proto.Message, error) {
			return c.ListJobs(ctx, &pb.ListJobsRequest{Source: "source", Repository: "o/r", Id: "7"})
		}, &pb.ListJobsResponse{}},
		{"jobs/9", func() (proto.Message, error) {
			v, err := c.GetJob(ctx, &pb.GetJobRequest{Source: "source", Repository: "o/r", Id: "9"})
			return v.GetJob(), err
		}, &pb.Job{}},
		{"jobs/9/log", func() (proto.Message, error) {
			v, err := c.ReadJobLog(ctx, &pb.ReadJobLogRequest{Source: "source", Repository: "o/r", Id: "9"})
			return v.GetLog(), err
		}, &pb.Log{}},
		{"pipelines/7/artifacts", func() (proto.Message, error) {
			return c.ListArtifacts(ctx, &pb.ListArtifactsRequest{Source: "source", Repository: "o/r", Id: "7"})
		}, &pb.ListArtifactsResponse{}},
		{"artifacts/11/download", func() (proto.Message, error) {
			return c.DownloadArtifact(ctx, &pb.DownloadArtifactRequest{Source: "source", Repository: "o/r", Id: "11"})
		}, &pb.DownloadArtifactResponse{}},
		{"definitions", func() (proto.Message, error) {
			return c.ListDefinitions(ctx, &pb.ListDefinitionsRequest{Source: "source", Repository: "o/r"})
		}, &pb.ListDefinitionsResponse{}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			want, err := tc.call()
			if err != nil {
				t.Fatal(err)
			}

			req := httptest.NewRequest("GET", "/v1/"+tc.path+"?source=source&repository=o/r", nil)
			req.Header.Set("Authorization", "Bearer service")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if err := protojson.Unmarshal(w.Body.Bytes(), tc.empty); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(want, tc.empty) {
				t.Fatalf("REST %v differs from gRPC %v", tc.empty, want)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
		})
	}
}

func TestAuthAndSafeErrors(t *testing.T) {
	s, h, c := apiFixture(t)
	for _, auth := range []string{"", "Bearer wrong"} {
		req := httptest.NewRequest("GET", "/v1/sources", nil)
		req.Header.Set("Authorization", auth)
		req.Header.Set("Grpc-Metadata-Authorization", "Bearer service")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("metadata bypass: %d", w.Code)
		}
	}

	if _, err := c.ListSources(t.Context(), &pb.ListSourcesRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer service"))
	if _, err := s.GetPipeline(ctx, &pb.GetPipelineRequest{
		Source:     "source",
		Repository: "o/r",
		Id:         "999",
	}); status.Code(err) != codes.PermissionDenied || strings.Contains(err.Error(), "sentinel") {
		t.Fatal(err)
	}
	if _, err := s.GetPipeline(ctx, &pb.GetPipelineRequest{
		Source:     "source",
		Repository: "o/r",
		Id:         "../secrets",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if status.Code(rpcError(actions.ErrUnsupported)) != codes.Unimplemented ||
		status.Code(rpcError(actions.ErrTooLarge)) != codes.ResourceExhausted {
		t.Fatal("wrong error mappings")
	}
}

func TestDeadlineAndTokenRotation(t *testing.T) {
	c, _ := actions.New()
	t.Setenv("ACTIONS_TEST_SERVICE_TOKEN", "old")
	s, err := New(func(ctx context.Context) (*actions.Client, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, forgeauth.EnvToken("ACTIONS_TEST_SERVICE_TOKEN"), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer old"))
	if _, err := s.ListSources(ctx, &pb.ListSourcesRequest{}); status.Code(err) != codes.DeadlineExceeded {
		t.Fatal(err)
	}

	s.sources = Fixed(c)
	t.Setenv("ACTIONS_TEST_SERVICE_TOKEN", "new")
	if _, err := s.ListSources(ctx, &pb.ListSourcesRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("rotation ignored", err)
	}

	ctx = metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer new"))
	if _, err := s.ListSources(ctx, &pb.ListSourcesRequest{}); err != nil {
		t.Fatal(err)
	}
}
