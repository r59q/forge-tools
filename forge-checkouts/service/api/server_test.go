package api

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-checkouts/library/checkouts"
	pb "github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"github.com/r59q/forge-tools/forge-checkouts/service/internal/testrepo"
	"github.com/r59q/forge-tools/forge-checkouts/service/storage"
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

func setup(t *testing.T) (*Server, http.Handler, pb.CheckoutsServiceClient) {
	t.Helper()
	root := t.TempDir()
	store, err := storage.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	manager, err := checkouts.New(context.Background(), root, store, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { manager.Close() })
	s, err := New(manager, forgeauth.StaticToken("test-only"), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	handler, err := s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterCheckoutsServiceServer(server, s)
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

	return s, handler, pb.NewCheckoutsServiceClient(conn)
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
	_, h, g := setup(t)
	repo := testrepo.New(t)
	var w pb.Checkout
	rest(t, h, "POST", "/v1/checkouts", &pb.CreateCheckoutRequest{Repository: &pb.Repository{LocalPath: repo}, Reference: "v1"}, &w)
	ctx := authorized()
	got, err := g.GetCheckout(ctx, &pb.GetCheckoutRequest{Id: w.Id})
	if err != nil || !proto.Equal(&w, got.GetCheckout()) {
		t.Fatal(got, err)
	}
	if err = os.WriteFile(filepath.Join(w.Location, "file"), []byte("staged\n"), 0600); err != nil {
		t.Fatal(err)
	}

	testrepo.Git(t, w.Location, "add", "file")
	if err = os.WriteFile(filepath.Join(w.Location, "file"), []byte("unstaged\n"), 0600); err != nil {
		t.Fatal(err)
	}

	inspected, err := g.InspectCheckout(ctx, &pb.InspectCheckoutRequest{Id: w.Id})
	st := inspected.GetStatus()
	if err != nil || len(st.GetFiles()) != 1 || st.Files[0].Index != "M" || st.Files[0].Worktree != "M" {
		t.Fatal(st, err)
	}

	var diff pb.Diff
	rest(t, h, "GET", "/v1/checkouts/"+w.Id+"/diff", nil, &diff)
	if !strings.Contains(string(diff.Staged), "+staged") || !strings.Contains(string(diff.Unstaged), "+unstaged") {
		t.Fatal(&diff)
	}

	gd, err := g.ReadDiff(ctx, &pb.ReadDiffRequest{Id: w.Id})
	if err != nil || !proto.Equal(&diff, gd.GetDiff()) {
		t.Fatal(gd, err)
	}

	var retained pb.Checkout
	rest(t, h, "POST", "/v1/checkouts/"+w.Id+"/retain", &pb.RetainCheckoutRequest{}, &retained)
	if retained.State != "retained" {
		t.Fatal(&retained)
	}

	list, err := g.ListCheckouts(ctx, &pb.ListCheckoutsRequest{})
	if err != nil || len(list.Checkouts) != 1 {
		t.Fatal(list, err)
	}

	var rs pb.Status
	rest(t, h, "GET", "/v1/checkouts/"+w.Id+"/status", nil, &rs)
	if !proto.Equal(st, &rs) {
		t.Fatal("status transport mismatch")
	}
	if _, err = g.RemoveCheckout(ctx, &pb.RemoveCheckoutRequest{Id: w.Id}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}

	rest(t, h, "DELETE", "/v1/checkouts/"+w.Id+"?discard=true", nil, &pb.RemoveCheckoutResponse{})
	if _, err = g.GetCheckout(ctx, &pb.GetCheckoutRequest{Id: w.Id}); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}

	var empty pb.ListCheckoutsResponse
	rest(t, h, "GET", "/v1/checkouts", nil, &empty)
	if len(empty.Checkouts) != 0 {
		t.Fatal(&empty)
	}
}

func TestAuthorizationAndValidation(t *testing.T) {
	_, h, g := setup(t)
	if _, err := g.ListCheckouts(context.Background(), &pb.ListCheckoutsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}

	for _, headers := range []map[string]string{{}, {"Grpc-Metadata-Authorization": "Bearer test-only"}, {"Authorization": "Bearer wrong"}} {
		req := httptest.NewRequest("POST", "/v1/checkouts", strings.NewReader("malformed secret input"))
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 401 || strings.Contains(w.Body.String(), "secret") {
			t.Fatal(w.Code, w.Body.String())
		}
	}

	if _, err := g.CreateCheckout(authorized(), &pb.CreateCheckoutRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/v1/checkouts", strings.NewReader(`{"secretUnknownField":true}`))
	req.Header.Set("Authorization", "Bearer test-only")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || strings.Contains(w.Body.String(), "secretUnknownField") {
		t.Fatal(w.Code, w.Body.String())
	}

	for _, id := range []string{"../escape", "", "-option"} {
		if _, err := g.GetCheckout(authorized(), &pb.GetCheckoutRequest{Id: id}); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
}
