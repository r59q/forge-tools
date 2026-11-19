package api

import (
	"context"
	"net/http"

	"github.com/r59q/forge-tools/forge-core/servicekit"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// The document RPCs allow 4 MiB of content plus paths and framing.
// JSON needs additional room for base64 content and escaped paths.
const (
	documentMessageBytes = 6 << 20
	documentBodyBytes    = 12 << 20
)

func bodyLimit(r *http.Request) int64 {
	if r.Method == http.MethodPost {
		switch r.URL.Path {
		case "/v1/documents:search", "/v1/documents:put":
			return documentBodyBytes
		}
	}
	return RequestBytes
}

// gRPC sets a receive limit per server. Retain the smaller request limit
// for methods that only carry names and query text.
func limitRequest(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	switch info.FullMethod {
	case pb.SearchService_SearchDocuments_FullMethodName, pb.SearchService_PutDocument_FullMethodName:
	default:
		if proto.Size(req.(proto.Message)) > RequestBytes {
			return nil, servicekit.Status(codes.ResourceExhausted)
		}
	}
	return handler(ctx, req)
}
