package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestAssetsAndAPIRouting(t *testing.T) {
	assets := fstest.MapFS{"index.html": {Data: []byte("explorer")}, "app.js": {Data: []byte("export {};")}}
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) })
	h := Handler(api, assets)
	for route, code := range map[string]int{"/": 200, "/app.js": 200, "/missing": 404, "/v1/explorer/sources": 401} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		if w.Code != code {
			t.Fatal(route, w.Code)
		}
		if code == 200 && w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("missing CSP")
		}
	}
}
