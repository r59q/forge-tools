package api

import (
	_ "embed"
	"html/template"
	"net/http"

	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

//go:embed index.html
var pageHTML string

//go:embed style.css
var style string

var page = template.Must(template.New("repositories").Parse(pageHTML))

func (s *Server) web(w http.ResponseWriter, r *http.Request) {
	// Replace incoming metadata with only the Basic credential. Neither proxy
	// headers nor query strings can supply operator authentication.
	user, password, ok := r.BasicAuth()
	ctx := metadata.NewIncomingContext(r.Context(), metadata.MD{})
	if ok && user == "operator" {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+password))
	}
	if err := s.authorize(ctx); err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="forge-tools-management", charset="UTF-8"`)
		http.Error(w, "Operator authentication required", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/style.css" {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(style))
		}
		return
	}
	listing, err := s.ListRepositories(ctx, nil)
	code := http.StatusOK
	if err != nil {
		code = http.StatusServiceUnavailable
		if status.Code(err) == codes.Unauthenticated {
			code = http.StatusUnauthorized
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_ = page.Execute(w, struct {
			Listing *pb.ListRepositoriesResponse
			Failed  bool
		}{listing, err != nil})
	}
}
