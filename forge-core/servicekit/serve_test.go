package servicekit

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
)

func TestServerRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	addrs := make(chan net.Addr, 1)
	server := Server{
		GRPCAddress: "127.0.0.1:0",
		HTTPAddress: "127.0.0.1:0",
		Register:    func(*grpc.Server) {},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}),
		WriteTimeout: time.Second,
		Ready:        func(_, httpAddr net.Addr) { addrs <- httpAddr },
	}

	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()

	body := get(t, "http://"+(<-addrs).String())
	if body != "ok" {
		t.Errorf("body = %q", body)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v", err)
	}
}

func TestServerRunListenError(t *testing.T) {
	server := Server{GRPCAddress: "256.0.0.1:0", HTTPAddress: "127.0.0.1:0"}
	if err := server.Run(t.Context()); err == nil || err.Error() != "cannot listen for gRPC" {
		t.Errorf("got %v", err)
	}
}

func TestShutdownNilServers(t *testing.T) {
	Shutdown(t.Context(), nil, nil)
}

func get(t *testing.T, url string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}
