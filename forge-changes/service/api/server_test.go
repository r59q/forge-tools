package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-changes/library/changes"
	pb "github.com/r59q/forge-tools/forge-changes/service/changespb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const proposal = `{"id":1,"number":7,"title":"Proposal","body":"Description","state":"open","head":{"ref":"feature","sha":"` + sha + `"},"base":{"ref":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"mergeable":true,"mergeable_state":"clean","changed_files":1}`

func setup(t *testing.T) (http.Handler, *grpc.ClientConn) {
	t.Helper()
	f := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/denied/"):
			http.Error(w, "private-upstream-sentinel", 403)
		case strings.HasSuffix(r.URL.Path, "/merge"):
			fmt.Fprint(w, `{"merged":true,"sha":"`+sha+`"}`)
		case strings.HasSuffix(r.URL.Path, "/comments"):
			cm := `{"id":3,"body":"comment","user":{"login":"sam"}}`
			if r.Method == "GET" {
				cm = "[" + cm + "]"
			}
			fmt.Fprint(w, cm)
		case strings.HasSuffix(r.URL.Path, "/commits"):
			fmt.Fprint(w, `[{"sha":"`+sha+`","commit":{"message":"Commit"}}]`)
		case strings.HasSuffix(r.URL.Path, "/files"):
			fmt.Fprint(w, `[{"filename":"a.go","status":"added","patch":"@@ -0,0 +1 @@\n+package a"}]`)
		case r.Header.Get("Accept") == "application/vnd.github.diff":
			fmt.Fprint(w, "diff --git a/a.go b/a.go\n")
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls"):
			fmt.Fprint(w, "["+proposal+"]")
		default:
			fmt.Fprint(w, proposal)
		}
	}))
	t.Cleanup(f.Close)
	c, err := changes.New(changes.Source{ID: "connection", Session: &forgeauth.Session{Kind: forgeauth.GitHub, BaseURL: f.URL, Client: f.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Fixed(c), forgeauth.StaticToken("service"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterChangesServer(g, s)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return h, conn
}

func TestRESTAndGRPCParity(t *testing.T) {
	h, conn := setup(t)
	r := &pb.ChangeRef{Source: "connection", Repository: "o/r", Number: 7}
	query := "?source=connection&repository=o%2Fr"
	tests := []struct {
		name, method, path string
		request, response  proto.Message
	}{
		{"ListSources", "GET", "/v1/sources", &emptypb.Empty{}, &pb.ListSourcesResponse{}},
		{"ListChanges", "GET", "/v1/changes" + query + "&draft=false", &pb.ListChangesRequest{Source: r.Source, Repository: r.Repository, Draft: ptr(false)}, &pb.ListChangesResponse{}},
		{"GetChange", "GET", "/v1/changes/7" + query, r, &pb.ChangeRequest{}},
		{"CreateChange", "POST", "/v1/changes", &pb.CreateChangeRequest{Source: r.Source, Repository: r.Repository, Title: "Proposal", SourceBranch: "feature", TargetBranch: "main"}, &pb.ChangeRequest{}},
		{"UpdateChange", "PATCH", "/v1/changes/7", &pb.UpdateChangeRequest{Source: r.Source, Repository: r.Repository, Number: 7, Description: ptr("")}, &pb.ChangeRequest{}},
		{"SetDraft", "PUT", "/v1/changes/7/draft", &pb.SetDraftRequest{Source: r.Source, Repository: r.Repository, Number: 7, Draft: false}, &pb.ChangeRequest{}},
		{"ListCommits", "GET", "/v1/changes/7/commits" + query, r, &pb.ListCommitsResponse{}},
		{"ListFiles", "GET", "/v1/changes/7/files" + query, r, &pb.Files{}},
		{"GetDiff", "GET", "/v1/changes/7/diff" + query, r, &pb.Diff{}},
		{"ListComments", "GET", "/v1/changes/7/comments" + query, r, &pb.ListCommentsResponse{}},
		{"AddComment", "POST", "/v1/changes/7/comments", &pb.AddCommentRequest{Source: r.Source, Repository: r.Repository, Number: 7, Body: "comment"}, &pb.Comment{}},
		{"GetReadiness", "GET", "/v1/changes/7/readiness" + query, r, &pb.Readiness{}},
		{"Merge", "POST", "/v1/changes/7/merge", &pb.MergeRequest{Source: r.Source, Repository: r.Repository, Number: 7, Method: "squash", ExpectedHead: sha}, &pb.MergeResult{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer service")
			if err := conn.Invoke(ctx, "/forge.changes.v1.Changes/"+tt.name, tt.request, tt.response); err != nil {
				t.Fatal(err)
			}
			body := []byte{}
			if tt.method != "GET" {
				var err error
				body, err = protojson.Marshal(tt.request)
				if err != nil {
					t.Fatal(err)
				}
			}
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer service")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("REST %d: %s", w.Code, w.Body.String())
			}
			v := tt.response.ProtoReflect().New().Interface()
			if err := protojson.Unmarshal(w.Body.Bytes(), v); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(v, tt.response) {
				t.Fatalf("REST %v != gRPC %v", v, tt.response)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Error("response cached")
			}
			// Every RPC authenticates before even resolving its sources.
			if err := conn.Invoke(context.Background(), "/forge.changes.v1.Changes/"+tt.name, tt.request, tt.response); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("unauthorized RPC: %v", err)
			}
			req = httptest.NewRequest(tt.method, tt.path, strings.NewReader(string(body)))
			req.Header.Set("Grpc-Metadata-Authorization", "Bearer service")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 401 {
				t.Fatalf("metadata bypass: %d", w.Code)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestErrorMappingAndRedaction(t *testing.T) {
	for _, tt := range []struct {
		err  error
		code codes.Code
	}{
		{changes.ErrUnsupported, codes.Unimplemented}, {changes.ErrInvalid, codes.InvalidArgument}, {changes.ErrUnknownSource, codes.InvalidArgument},
		{changes.ErrHeadChanged, codes.FailedPrecondition}, {changes.ErrMergeRejected, codes.FailedPrecondition}, {changes.ErrTooLarge, codes.ResourceExhausted},
		{forgehttp.ErrForbidden, codes.PermissionDenied}, {forgehttp.ErrNotFound, codes.NotFound}, {context.DeadlineExceeded, codes.DeadlineExceeded},
	} {
		if got := status.Code(rpcError(tt.err)); got != tt.code {
			t.Errorf("%v: %v", tt.err, got)
		}
	}
	h, _ := setup(t)
	for _, tt := range []struct {
		method, path, body string
		code               int
	}{
		{"GET", "/v1/changes/7?source=connection&repository=denied/r", "", 403},
		{"PATCH", "/v1/changes/7", `{"source":"connection","repository":"o/r","sourceBranch":"new"}`, 501},
		{"POST", "/v1/changes/7/merge", `{"source":"connection","repository":"o/r","expectedHead":"cccccccccccccccccccccccccccccccccccccccc"}`, 400},
		{"PATCH", "/v1/changes/7", `{"source":"connection","repository":"o/r","unknown":"input-sentinel"}`, 400},
	} {
		r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		r.Header.Set("Authorization", "Bearer service")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tt.code || strings.Contains(w.Body.String(), "sentinel") {
			t.Errorf("%s: %d %s", tt.path, w.Code, w.Body.String())
		}
	}
}

func TestDeadlineAndNilSource(t *testing.T) {
	for _, nilClient := range []bool{false, true} {
		s, err := New(func(ctx context.Context) (*changes.Client, error) {
			if nilClient {
				return nil, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}, forgeauth.StaticToken("service"), time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer service"))
		_, err = s.ListSources(ctx, &emptypb.Empty{})
		want := codes.DeadlineExceeded
		if nilClient {
			want = codes.Unavailable
		}
		if status.Code(err) != want {
			t.Fatal(err)
		}
	}
}

func TestRESTBodyLimit(t *testing.T) {
	h, _ := setup(t)
	r := httptest.NewRequest("POST", "/v1/changes", io.LimitReader(strings.NewReader(`{"description":"`+strings.Repeat("x", 2<<20)+`"}`), 3<<20))
	r.Header.Set("Authorization", "Bearer service")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatal("oversized body accepted")
	}
}
