package checkouts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}

	return hex.EncodeToString(b)
}

// fetch bridges Git's HTTP requests to the ready authenticated client. The
// temporary loopback endpoint accepts only fetch requests for this repository.
// Credentials never enter Git's environment, arguments or repository config.
func fetch(ctx context.Context, remote *forgeauth.GitRemote, repository, directory, reference string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	endpoint, err := remoteEndpoint(remote, repository)
	if err != nil {
		return err
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}

	prefix := "/" + newID()
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler:           &bridge{client: remote.Client, endpoint: endpoint, prefix: prefix},
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	defer func() {
		cancel()
		_ = server.Close()
		<-done
	}()

	_, err = git(
		ctx,
		directory,
		"-c",
		"protocol.allow=never",
		"-c",
		"protocol.http.allow=always",
		"-c",
		"http.proxy=",
		"-c",
		"http.followRedirects=false",
		"fetch",
		"--no-tags",
		"--no-recurse-submodules",
		"--",
		"http://"+listener.Addr().String()+prefix,
		reference,
	)

	return err
}

// remoteEndpoint returns the HTTP(S) Git URL of repository on remote.
func remoteEndpoint(remote *forgeauth.GitRemote, repository string) (string, error) {
	if remote == nil || remote.Client == nil {
		return "", ErrInvalid
	}

	endpoint, err := remote.URL(repository)
	if err != nil {
		return "", ErrInvalid
	}

	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", ErrInvalid
	}

	return endpoint, nil
}

// bridge forwards Git fetch requests under prefix to endpoint with client.
// Every other request is not found.
type bridge struct {
	client   *http.Client
	endpoint string
	prefix   string
}

// ServeHTTP forwards one fetch request and copies back the response.
func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method, suffix, ok := b.route(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), method, b.endpoint+suffix, http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		http.Error(w, "request failed", http.StatusBadGateway)
		return
	}

	req.ContentLength = r.ContentLength
	if method == http.MethodGet {
		req.Body = nil
		req.ContentLength = 0
	}

	for _, h := range []string{"Content-Type", "Content-Encoding", "Accept", "Git-Protocol"} {
		if value := r.Header.Get(h); value != "" {
			req.Header.Set(h, value)
		}
	}

	resp, err := b.client.Do(req)
	if err != nil {
		http.Error(w, "fetch failed", http.StatusBadGateway)
		return
	}

	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// route returns the method and endpoint suffix of a fetch request, or false
// for any other request.
func (b *bridge) route(r *http.Request) (string, string, bool) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == b.prefix+"/info/refs" && r.URL.RawQuery == "service=git-upload-pack":
		return http.MethodGet, "/info/refs?service=git-upload-pack", true
	case r.Method == http.MethodPost && r.URL.Path == b.prefix+"/git-upload-pack" && r.URL.RawQuery == "":
		return http.MethodPost, "/git-upload-pack", true
	}

	return "", "", false
}
