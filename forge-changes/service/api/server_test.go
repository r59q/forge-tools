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
	"google.golang.org/protobuf/reflect/protoreflect"
)

const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const proposal = `{"id":1,"number":7,"title":"Proposal","body":"Description","state":"open","head":{"ref":"feature","sha":"` + sha + `"},"base":{"ref":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"mergeable":true,"mergeable_state":"clean","changed_files":1}`

func setup(t *testing.T) (http.Handler, *grpc.ClientConn) {
	t.Helper()
	return setupForge(t, forgeauth.GitHub, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/denied/"):
			http.Error(w, "private-upstream-sentinel", http.StatusForbidden)
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
	})
}

func setupForge(t *testing.T, kind forgeauth.Kind, handler http.HandlerFunc) (http.Handler, *grpc.ClientConn) {
	t.Helper()
	f := httptest.NewServer(handler)
	t.Cleanup(f.Close)
	c, err := changes.New(changes.Source{ID: "connection", Session: &forgeauth.Session{Kind: kind, BaseURL: f.URL, Client: f.Client()}})
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
	pb.RegisterChangesServiceServer(g, s)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return h, conn
}

func TestLockedProposalTransports(t *testing.T) {
	const locked = `{"id":1,"iid":7,"title":"Merging","state":"locked","detailed_merge_status":"mergeable"}`
	h, conn := setupForge(t, forgeauth.GitLab, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/merge_requests") {
			fmt.Fprint(w, "["+locked+"]")
		} else {
			fmt.Fprint(w, locked)
		}
	})
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer service")
	r := &pb.ChangeRef{Source: "connection", Repository: "o/r", Number: 7}
	query := "?source=connection&repository=o%2Fr"
	for _, tt := range []struct {
		method, path      string
		request, response proto.Message
	}{
		{"GetChange", "/v1/changes/7" + query, named(&pb.GetChangeRequest{}), &pb.GetChangeResponse{}},
		{"ListChanges", "/v1/changes" + query + "&state=all", &pb.ListChangesRequest{
			Source:     r.Source,
			Repository: r.Repository,
			State:      "all",
		}, &pb.ListChangesResponse{}},
		{"GetReadiness", "/v1/changes/7/readiness" + query, named(&pb.GetReadinessRequest{}), &pb.GetReadinessResponse{}},
	} {
		t.Run(tt.method, func(t *testing.T) {
			if err := conn.Invoke(ctx, "/forge.changes.v1.ChangesService/"+tt.method, tt.request, tt.response); err != nil {
				t.Fatal(err)
			}

			switch v := tt.response.(type) {
			case *pb.GetChangeResponse:
				if v.GetChange().GetState() != "locked" {
					t.Fatalf("state = %q", v.GetChange().GetState())
				}
			case *pb.ListChangesResponse:
				if len(v.Changes) != 1 || v.Changes[0].State != "locked" {
					t.Fatalf("list = %v", v)
				}
			case *pb.GetReadinessResponse:
				if v.Status != "blocked" {
					t.Fatalf("readiness = %v", v)
				}
			}

			req := httptest.NewRequest("GET", tt.path, nil)
			req.Header.Set("Authorization", "Bearer service")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("REST %d: %s", w.Code, w.Body.String())
			}

			want := restBody(tt.response)
			v := want.ProtoReflect().New().Interface()
			if err := protojson.Unmarshal(w.Body.Bytes(), v); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(v, want) {
				t.Fatalf("REST %v != gRPC %v", v, want)
			}
		})
	}
}

func TestRESTAndGRPCParity(t *testing.T) {
	h, conn := setup(t)
	r := &pb.ChangeRef{Source: "connection", Repository: "o/r", Number: 7}
	query := "?source=connection&repository=o%2Fr"
	tests := []struct {
		name              string
		method            string
		path              string
		request, response proto.Message
	}{
		{"ListSources", "GET", "/v1/sources", &pb.ListSourcesRequest{}, &pb.ListSourcesResponse{}},
		{"ListChanges", "GET", "/v1/changes" + query + "&draft=false", &pb.ListChangesRequest{
			Source:     r.Source,
			Repository: r.Repository,
			Draft:      ptr(false),
		}, &pb.ListChangesResponse{}},
		{"GetChange", "GET", "/v1/changes/7" + query, named(&pb.GetChangeRequest{}), &pb.GetChangeResponse{}},
		{"CreateChange", "POST", "/v1/changes", &pb.CreateChangeRequest{
			Source:       r.Source,
			Repository:   r.Repository,
			Title:        "Proposal",
			SourceBranch: "feature",
			TargetBranch: "main",
		}, &pb.CreateChangeResponse{}},
		{"UpdateChange", "PATCH", "/v1/changes/7", &pb.UpdateChangeRequest{
			Source:      r.Source,
			Repository:  r.Repository,
			Number:      7,
			Description: ptr(""),
		}, &pb.UpdateChangeResponse{}},
		{"SetDraft", "PUT", "/v1/changes/7/draft", &pb.SetDraftRequest{
			Source:     r.Source,
			Repository: r.Repository,
			Number:     7,
			Draft:      false,
		}, &pb.SetDraftResponse{}},
		{"ListCommits", "GET", "/v1/changes/7/commits" + query, named(&pb.ListCommitsRequest{}), &pb.ListCommitsResponse{}},
		{"ListFiles", "GET", "/v1/changes/7/files" + query, named(&pb.ListFilesRequest{}), &pb.ListFilesResponse{}},
		{"GetDiff", "GET", "/v1/changes/7/diff" + query, named(&pb.GetDiffRequest{}), &pb.GetDiffResponse{}},
		{"ListComments", "GET", "/v1/changes/7/comments" + query, named(&pb.ListCommentsRequest{}), &pb.ListCommentsResponse{}},
		{"AddComment", "POST", "/v1/changes/7/comments", &pb.AddCommentRequest{
			Source:     r.Source,
			Repository: r.Repository,
			Number:     7,
			Body:       "comment",
		}, &pb.AddCommentResponse{}},
		{"GetReadiness", "GET", "/v1/changes/7/readiness" + query, named(&pb.GetReadinessRequest{}), &pb.GetReadinessResponse{}},
		{"Merge", "POST", "/v1/changes/7/merge", &pb.MergeRequest{
			Source:       r.Source,
			Repository:   r.Repository,
			Number:       7,
			Method:       "squash",
			ExpectedHead: sha,
		}, &pb.MergeResponse{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer service")
			if err := conn.Invoke(ctx, "/forge.changes.v1.ChangesService/"+tt.name, tt.request, tt.response); err != nil {
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

			want := restBody(tt.response)
			v := want.ProtoReflect().New().Interface()
			if err := protojson.Unmarshal(w.Body.Bytes(), v); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(v, want) {
				t.Fatalf("REST %v != gRPC %v", v, want)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Error("response cached")
			}
			// Every RPC authenticates before even resolving its sources.
			if err := conn.Invoke(
				context.Background(),
				"/forge.changes.v1.ChangesService/"+tt.name,
				tt.request,
				tt.response,
			); status.Code(err) != codes.Unauthenticated {
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

// named sets a request's source, repository and number to the test proposal.
func named[T proto.Message](request T) T {
	m := request.ProtoReflect()
	fields := m.Descriptor().Fields()
	m.Set(fields.ByName("source"), protoreflect.ValueOfString("connection"))
	m.Set(fields.ByName("repository"), protoreflect.ValueOfString("o/r"))
	m.Set(fields.ByName("number"), protoreflect.ValueOfInt64(7))

	return request
}

// restBody returns the part of a gRPC response that REST returns.
func restBody(response proto.Message) proto.Message {
	switch v := response.(type) {
	case interface{ GetChange() *pb.ChangeRequest }:
		return v.GetChange()
	case interface{ GetComment() *pb.Comment }:
		return v.GetComment()
	}

	return response
}

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
		method string
		path   string
		body   string
		code   int
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
		_, err = s.ListSources(ctx, &pb.ListSourcesRequest{})
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
	r := httptest.NewRequest(
		"POST",
		"/v1/changes",
		io.LimitReader(strings.NewReader(`{"description":"`+strings.Repeat("x", 2<<20)+`"}`), 3<<20),
	)
	r.Header.Set("Authorization", "Bearer service")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatal("oversized body accepted")
	}
}
