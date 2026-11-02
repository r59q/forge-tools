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
	if remote == nil || remote.Client == nil {
		return ErrInvalid
	}
	endpoint, err := remote.URL(repository)
	if err != nil {
		return ErrInvalid
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ErrInvalid
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	prefix := "/" + newID()
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var suffix string
		switch {
		case r.Method == "GET" && r.URL.Path == prefix+"/info/refs" && r.URL.RawQuery == "service=git-upload-pack":
			suffix = "/info/refs?service=git-upload-pack"
		case r.Method == "POST" && r.URL.Path == prefix+"/git-upload-pack" && r.URL.RawQuery == "":
			suffix = "/git-upload-pack"
		default:
			http.NotFound(w, r)
			return
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, endpoint+suffix, http.MaxBytesReader(w, r.Body, 16<<20))
		if err != nil {
			http.Error(w, "request failed", 502)
			return
		}
		req.ContentLength = r.ContentLength
		if r.Method == http.MethodGet {
			req.Body = nil
			req.ContentLength = 0
		}
		for _, h := range []string{"Content-Type", "Content-Encoding", "Accept", "Git-Protocol"} {
			if value := r.Header.Get(h); value != "" {
				req.Header.Set(h, value)
			}
		}
		resp, err := remote.Client.Do(req)
		if err != nil {
			http.Error(w, "fetch failed", 502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { cancel(); _ = server.Close(); <-done }()
	_, err = git(ctx, directory, "-c", "protocol.allow=never", "-c", "protocol.http.allow=always", "-c", "http.proxy=", "-c", "http.followRedirects=false", "fetch", "--no-tags", "--no-recurse-submodules", "--", "http://"+listener.Addr().String()+prefix, reference)
	return err
}
