package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-review/library/review"
	pb "github.com/r59q/forge-tools/forge-review/service/reviewpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const testPatch = "--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n"

func setup(t *testing.T, sources Sources) (*Server, http.Handler, pb.ReviewServiceClient) {
	t.Helper()
	s, err := New(&review.MemoryStore{}, sources, forgeauth.StaticToken("test-only"), time.Second)
	if err != nil {
		t.Fatal(err)
	}

	handler, err := s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterReviewServiceServer(server, s)
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient(
		"passthrough:///buf",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		conn.Close()
		server.Stop()
		listener.Close()
	})

	return s, handler, pb.NewReviewServiceClient(conn)
}

func authorized() context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer test-only")
}

func rest(t *testing.T, h http.Handler, method, path string, in, out proto.Message) {
	t.Helper()
	var data []byte
	var err error
	if in != nil {
		data, err = protojson.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer test-only")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cache enabled")
	}
	if err = protojson.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}

func TestRESTAndGRPCLifecycle(t *testing.T) {
	calls := 0
	_, h, g := setup(t, func(context.Context) ([]review.Source, error) {
		calls++
		return nil, errors.New("connector offline")
	})
	var subject pb.Subject
	rest(t, h, "POST", "/v1/subjects", &pb.CreateSubjectRequest{Kind: "patch", Patch: testPatch}, &subject)
	if subject.Id == "" || len(subject.Files) != 1 || subject.Files[0].Path != "a" {
		t.Fatal(&subject)
	}

	ctx := authorized()
	snapshot, err := g.GetSubject(ctx, &pb.GetSubjectRequest{SubjectId: subject.Id})
	if err != nil || !proto.Equal(&subject, snapshot.GetSubject()) {
		t.Fatal(snapshot, err)
	}

	requested, err := g.RequestReview(ctx, &pb.RequestReviewRequest{SubjectId: subject.Id, Reviewer: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	assignment := requested.GetReview()

	var commented pb.Review
	rest(t, h, "POST", "/v1/reviews/"+assignment.Id+"/threads", &pb.AddCommentRequest{
		Author: "alice",
		Body:   "why?",
		Anchor: &pb.Anchor{
			Path:  "a",
			Side:  "new",
			Start: 1,
			End:   1,
		},
	}, &commented)
	tid := commented.Threads[0].Id
	replied, err := g.Reply(ctx, &pb.ReplyRequest{
		ReviewId: assignment.Id,
		ThreadId: tid,
		Author:   "bob",
		Body:     "because",
	})
	if err != nil || len(replied.GetReview().Threads[0].Comments) != 2 {
		t.Fatal(replied, err)
	}

	var resolved pb.Review
	rest(t, h, "PUT", "/v1/reviews/"+assignment.Id+"/threads/"+tid+"/resolution", &pb.ResolveRequest{Resolved: true}, &resolved)
	if !resolved.Threads[0].Resolved {
		t.Fatal(&resolved)
	}

	submitted, err := g.Submit(ctx, &pb.SubmitRequest{ReviewId: assignment.Id, Status: "approved"})
	if err != nil || submitted.GetReview().SubmittedAt == nil {
		t.Fatal(submitted, err)
	}

	var got pb.Review
	rest(t, h, "GET", "/v1/reviews/"+assignment.Id, nil, &got)
	if !proto.Equal(submitted.GetReview(), &got) {
		t.Fatal("transport mismatch")
	}

	var listed pb.ListReviewsResponse
	rest(t, h, "GET", "/v1/reviews?subjectId="+subject.Id+"&reviewer=alice&status=approved", nil, &listed)
	if len(listed.Reviews) != 1 {
		t.Fatal(&listed)
	}
	if calls != 0 {
		t.Fatal("standalone operation needed connector")
	}
	if _, err = g.AddComment(ctx, &pb.AddCommentRequest{
		ReviewId: assignment.Id,
		Author:   "alice",
		Body:     "late",
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err = g.GetSubject(ctx, &pb.GetSubjectRequest{SubjectId: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
}

func TestAuthInvalidInputAndNativeErrors(t *testing.T) {
	_, h, g := setup(t, nil)
	if _, err := g.ListReviews(context.Background(), &pb.ListReviewsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}

	for _, headers := range []map[string]string{{}, {"Grpc-Metadata-Authorization": "Bearer test-only"}, {"Authorization": "Bearer wrong"}} {
		req := httptest.NewRequest("GET", "/v1/reviews", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatal(w.Code, w.Body.String())
		}
	}

	req := httptest.NewRequest("POST", "/v1/subjects", strings.NewReader(`{"kind":"patch","unknown":"sensitive"}`))
	req.Header.Set("Authorization", "Bearer test-only")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || strings.Contains(w.Body.String(), "sensitive") {
		t.Fatal(w.Code, w.Body.String())
	}

	ctx := authorized()
	created, err := g.CreateSubject(ctx, &pb.CreateSubjectRequest{Kind: "patch", Patch: testPatch})
	if err != nil {
		t.Fatal(err)
	}

	r, err := g.RequestReview(ctx, &pb.RequestReviewRequest{SubjectId: created.GetSubject().Id, Reviewer: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.AddComment(ctx, &pb.AddCommentRequest{
		ReviewId: r.GetReview().Id,
		Author:   "a",
		Body:     "bad",
		Anchor: &pb.Anchor{
			Path:  "a",
			Side:  "old",
			Start: 2,
			End:   2,
		},
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if _, err = g.SubmitNative(ctx, &pb.SubmitNativeRequest{SubjectId: created.GetSubject().Id, Status: "approved"}); status.Code(err) != codes.Unimplemented {
		t.Fatal(err)
	}
	if _, err = g.GetCapabilities(ctx, &pb.GetCapabilitiesRequest{Source: "x"}); status.Code(err) != codes.Unimplemented {
		t.Fatal(err)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConnectedTransportAndNativeResponse(t *testing.T) {
	const head = "2222222222222222222222222222222222222222"
	const base = "1111111111111111111111111111111111111111"
	source := review.Source{ID: "src", Session: &forgeauth.Session{
		Kind:    forgeauth.GitHub,
		BaseURL: "https://fixture.test",
		Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			body := `{"head":{"sha":"` + head + `"},"base":{"sha":"` + base + `"}}`
			if r.Header.Get("Accept") == "application/vnd.github.diff" {
				body = testPatch
			}
			if r.Method == "POST" {
				body = `{"id":42,"state":"APPROVED","commit_id":"` + head + `"}`
			}
			if strings.HasSuffix(r.URL.Path, "/reviews") && r.Method == "GET" {
				body = `[{"id":42,"state":"APPROVED"}]`
			}

			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		})},
	}}
	_, h, g := setup(t, func(context.Context) ([]review.Source, error) { return []review.Source{source}, nil })
	var s pb.Subject
	rest(t, h, "POST", "/v1/subjects", &pb.CreateSubjectRequest{Kind: "change_request", Ref: &pb.Ref{
		Source:     "src",
		Repository: "owner/repo",
		Number:     5,
	}}, &s)
	if s.Ref.Number != 5 || s.Revision.Head != head {
		t.Fatal(&s)
	}

	result, err := g.SubmitNative(authorized(), &pb.SubmitNativeRequest{SubjectId: s.Id, Status: "approved"})
	if err != nil || result.GetResult().Storage != "native" || !strings.Contains(result.GetResult().DataJson, head) {
		t.Fatal(result, err)
	}

	var native pb.NativeResult
	rest(t, h, "GET", "/v1/subjects/"+s.Id+"/native/reviews", nil, &native)
	if !strings.Contains(native.DataJson, "APPROVED") {
		t.Fatal(&native)
	}

	var caps pb.Capabilities
	rest(t, h, "GET", "/v1/sources/src/capabilities", nil, &caps)
	if !caps.RequestChanges || caps.Resolve {
		t.Fatal(&caps)
	}
}
