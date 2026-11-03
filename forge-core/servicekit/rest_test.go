package servicekit

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSecure(t *testing.T) {
	var read error
	h := Secure(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, read = io.ReadAll(r.Body)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(strings.Repeat("x", maxBody+1)))
	h.ServeHTTP(w, r)

	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", w.Header())
	}

	if read == nil {
		t.Error("oversized body was read")
	}
}

func TestRESTErrorHidesMessage(t *testing.T) {
	mux := NewMux(false)
	marshaler := &runtime.JSONPb{}
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

	RESTError(t.Context(), mux, marshaler, w, r, status.Error(codes.NotFound, "secret path /x"))
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "secret") {
		t.Errorf("got %d %s", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	RESTError(t.Context(), mux, marshaler, w, r, io.EOF)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "EOF") {
		t.Errorf("got %d %s", w.Code, w.Body)
	}
}
