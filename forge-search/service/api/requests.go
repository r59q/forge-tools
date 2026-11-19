package api

import (
	"bytes"
	"context"
	"errors"
	"io"
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

// Check unknown-length bodies before dispatch. The gateway can stop reading
// after the first JSON value, even when the stream exceeds the route limit.
func limitBody(w http.ResponseWriter, r *http.Request) error {
	limit := bodyLimit(r)
	if r.ContentLength > limit {
		return servicekit.Status(codes.ResourceExhausted)
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if r.ContentLength >= 0 {
		return nil
	}

	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return servicekit.Status(codes.ResourceExhausted)
	case err != nil:
		return servicekit.Status(codes.InvalidArgument)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return nil
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
