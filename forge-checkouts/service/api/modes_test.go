package api

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"github.com/r59q/forge-tools/forge-checkouts/library/checkouts"
	pb "github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"github.com/r59q/forge-tools/forge-checkouts/service/internal/testrepo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestConnectedModesOverRESTAndGRPC(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			repo := testrepo.New(t, "--object-format="+format)
			remote := httptest.NewServer(testrepo.HTTP(t, repo, "/"))
			defer remote.Close()
			_, handler, client := setup(t, func(context.Context, string) (*forgeauth.GitRemote, error) {
				return &forgeauth.GitRemote{BaseURL: remote.URL, Client: remote.Client()}, nil
			})
			for _, mode := range []string{checkouts.Worktree, checkouts.Isolated} {
				in := &pb.CreateCheckoutRequest{Repository: &pb.Repository{Source: "fixture", Path: "owner/repo"}, Mode: mode}
				var restCopy pb.Checkout
				rest(t, handler, "POST", "/v1/checkouts", in, &restCopy)
				grpcCopy, err := client.CreateCheckout(authorized(), in)
				if err != nil || grpcCopy.GetCheckout().GetMode() != mode || restCopy.Mode != mode {
					t.Fatal(&restCopy, grpcCopy, err)
				}
				for _, copy := range []*pb.Checkout{&restCopy, grpcCopy.Checkout} {
					var got pb.Checkout
					rest(t, handler, "GET", "/v1/checkouts/"+copy.Id, nil, &got)
					if !proto.Equal(copy, &got) {
						t.Fatal("mode or metadata lost", &got)
					}
					testrepo.Git(t, copy.Location, "switch", "-c", "branch-"+copy.Id)
					testrepo.Git(t, copy.Location, "commit", "--allow-empty", "-m", "consumer")
					st, err := client.InspectCheckout(authorized(), &pb.InspectCheckoutRequest{Id: copy.Id})
					if err != nil || st.GetStatus().GetRevision() == copy.Revision || st.GetStatus().GetBranch() != "branch-"+copy.Id {
						t.Fatal(st, err)
					}
					var diff pb.Diff
					rest(t, handler, "GET", "/v1/checkouts/"+copy.Id+"/diff", nil, &diff)
					if len(diff.Staged)+len(diff.Unstaged) != 0 {
						t.Fatal(&diff)
					}
					retained, err := client.RetainCheckout(authorized(), &pb.RetainCheckoutRequest{Id: copy.Id})
					if err != nil || retained.GetCheckout().GetMode() != mode {
						t.Fatal(retained, err)
					}
					rest(t, handler, "DELETE", "/v1/checkouts/"+copy.Id, nil, &pb.RemoveCheckoutResponse{})
				}
			}
		})
	}
}

func TestModeValidation(t *testing.T) {
	_, handler, client := setup(t)
	for _, mode := range []string{checkouts.Isolated, "unknown"} {
		in := &pb.CreateCheckoutRequest{Repository: &pb.Repository{LocalPath: testrepo.New(t)}, Mode: mode}
		if _, err := client.CreateCheckout(authorized(), in); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
		data, err := protojson.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/v1/checkouts", bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer test-only")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != 400 {
			t.Fatal(out.Code, out.Body.String())
		}
	}
}
