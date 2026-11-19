// Package api exposes forge-chat presentation metadata over REST and gRPC.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	pb "github.com/r59q/forge-tools/forge-chat/service/chatpb"
	"github.com/r59q/forge-tools/forge-chat/service/store"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc/codes"
)

// Metadata serves the chat preferences shared by the users of a deployment.
type Metadata struct {
	pb.UnimplementedChatServiceServer
	data    *store.Store
	token   forgeauth.Credentials
	timeout time.Duration
}

// NewMetadata creates the authenticated metadata API.
func NewMetadata(data *store.Store, token forgeauth.Credentials, timeout time.Duration) *Metadata {
	return &Metadata{data: data, token: token, timeout: timeout}
}

// REST returns the generated HTTP routes for the metadata API.
func (s *Metadata) REST(ctx context.Context) (http.Handler, error) {
	gateway := servicekit.NewMux(true)
	if err := pb.RegisterChatServiceHandlerServer(ctx, gateway, s); err != nil {
		return nil, err
	}
	return gateway, nil
}

// ListChatMetadata returns the saved UI preferences.
func (s *Metadata) ListChatMetadata(ctx context.Context, _ *pb.ListChatMetadataRequest) (*pb.ListChatMetadataResponse, error) {
	return servicekit.Call(ctx, s.rpc(), func(ctx context.Context, data *store.Store) (*pb.ListChatMetadataResponse, error) {
		records, err := data.List(ctx)
		return &pb.ListChatMetadataResponse{Chats: records}, err
	})
}

// UpdateChatMetadata updates the supplied UI preference fields.
func (s *Metadata) UpdateChatMetadata(ctx context.Context, req *pb.UpdateChatMetadataRequest) (*pb.UpdateChatMetadataResponse, error) {
	return servicekit.Call(ctx, s.rpc(), func(ctx context.Context, data *store.Store) (*pb.UpdateChatMetadataResponse, error) {
		record, err := data.Patch(ctx, req)
		return &pb.UpdateChatMetadataResponse{Chat: record}, err
	})
}

func (s *Metadata) rpc() servicekit.RPC[*store.Store] {
	return servicekit.RPC[*store.Store]{
		Token:   s.token,
		Timeout: s.timeout,
		Client:  func(context.Context) (*store.Store, error) { return s.data, nil },
		Code: func(err error) codes.Code {
			if errors.Is(err, store.ErrInvalid) {
				return codes.InvalidArgument
			}
			return servicekit.ForgeCode(err)
		},
	}
}
