package servicekit

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
)

// shutdownGrace is how long in-flight requests may run during shutdown.
const shutdownGrace = 5 * time.Second

// Server is a service's gRPC and REST listeners.
type Server struct {
	GRPCAddress, HTTPAddress string
	// Register adds the gRPC services.
	Register func(*grpc.Server)
	// GRPCOptions replace the default 1 MiB receive limit.
	GRPCOptions []grpc.ServerOption
	// Handler serves REST.
	Handler http.Handler
	// WriteTimeout bounds each REST response.
	WriteTimeout time.Duration
	// Ready, if set, is called with the bound addresses once both serve.
	Ready func(grpcAddr, httpAddr net.Addr)
}

// Run serves until ctx ends or a listener fails, then shuts down
// gracefully. It returns nil when ctx ends.
func (s Server) Run(ctx context.Context) error {
	listen := &net.ListenConfig{}
	grpcListener, err := listen.Listen(ctx, "tcp", s.GRPCAddress)
	if err != nil {
		return errors.New("cannot listen for gRPC")
	}

	defer func() { _ = grpcListener.Close() }()

	httpListener, err := listen.Listen(ctx, "tcp", s.HTTPAddress)
	if err != nil {
		return errors.New("cannot listen for REST")
	}

	defer func() { _ = httpListener.Close() }()

	grpcServer := grpc.NewServer(s.grpcOptions()...)
	s.Register(grpcServer)
	httpServer := NewHTTPServer(s.Handler, s.WriteTimeout)

	failures := make(chan error, 2)
	go func() { failures <- grpcServer.Serve(grpcListener) }()
	go func() { failures <- httpServer.Serve(httpListener) }()

	if s.Ready != nil {
		s.Ready(grpcListener.Addr(), httpListener.Addr())
	}

	select {
	case <-ctx.Done():
		err = nil
	case failure := <-failures:
		err = fmt.Errorf("listener stopped unexpectedly: %w", failure)
	}

	Shutdown(ctx, grpcServer, httpServer)

	return err
}

// grpcOptions returns the configured options or the defaults.
func (s Server) grpcOptions() []grpc.ServerOption {
	if s.GRPCOptions != nil {
		return s.GRPCOptions
	}

	return []grpc.ServerOption{grpc.MaxRecvMsgSize(1 << 20)}
}

// NewHTTPServer returns an HTTP server with conservative timeouts and
// header limits.
func NewHTTPServer(handler http.Handler, writeTimeout time.Duration) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

// Shutdown drains in-flight requests for up to five seconds, then stops
// both servers. Either may be nil.
func Shutdown(ctx context.Context, grpcServer *grpc.Server, httpServer *http.Server) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()

	stopped := make(chan struct{})
	go func() {
		if grpcServer != nil {
			grpcServer.GracefulStop()
		}

		close(stopped)
	}()

	if httpServer != nil {
		_ = httpServer.Shutdown(ctx)
	}

	select {
	case <-stopped:
	case <-ctx.Done():
		if grpcServer != nil {
			grpcServer.Stop()
		}
	}

	if httpServer != nil {
		_ = httpServer.Close()
	}
}

// Main runs start until SIGINT or SIGTERM. It exits with status 1 after
// printing any error other than flag.ErrHelp.
func Main(start func(context.Context) error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := start(ctx)
	stop()

	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
