package web

import (
	"io/fs"
	"net/http"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
)

// Handler serves the public HTML shell and assets alongside authenticated API routes.
func Handler(assets fs.FS, metadata http.Handler, agents *AgentProxy, token forgeauth.Credentials) http.Handler {
	routes := http.NewServeMux()
	files := http.FileServerFS(assets)
	routes.Handle("GET /v1/chat-metadata", authorize(token, metadata))
	routes.Handle("PATCH /v1/chat-metadata/{id}", authorize(token, metadata))
	agents.routes(routes, token)
	routes.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		asset(w, r, assets, files, "index.html")
	})
	for _, dir := range []string{"app", "src"} {
		routes.HandleFunc("GET /"+dir+"/{file}", func(w http.ResponseWriter, r *http.Request) {
			asset(w, r, assets, files, dir+"/"+r.PathValue("file"))
		})
	}
	return servicekit.Secure(routes)
}

func asset(w http.ResponseWriter, r *http.Request, assets fs.FS, files http.Handler, name string) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'")
	info, err := fs.Stat(assets, name)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	files.ServeHTTP(w, r)
}
