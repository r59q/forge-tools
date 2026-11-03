package servicekit

import (
	"context"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// maxBody caps REST request bodies.
const maxBody = 1 << 20

// NewMux returns a grpc-gateway mux that rejects unknown JSON fields,
// forwards no headers as metadata and replies to errors with only a status.
// With emitUnpopulated, responses include fields with zero values.
func NewMux(emitUnpopulated bool) *runtime.ServeMux {
	marshaler := &runtime.JSONPb{
		MarshalOptions:   protojson.MarshalOptions{EmitUnpopulated: emitUnpopulated},
		UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: false},
	}

	return runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, marshaler),
		runtime.WithIncomingHeaderMatcher(func(string) (string, bool) { return "", false }),
		runtime.WithErrorHandler(RESTError),
	)
}

// RESTError replies with only the status of err, never its message.
// Unknown errors become Internal.
func RESTError(
	ctx context.Context,
	mux *runtime.ServeMux,
	marshaler runtime.Marshaler,
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	code := status.Code(err)
	if code == codes.Unknown {
		code = codes.Internal
	}

	runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, Status(code))
}

// Secure wraps next so responses are never cached or sniffed and request
// bodies are capped at 1 MiB.
func Secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}
