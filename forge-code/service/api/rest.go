package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/r59q/forge-tools/forge-code/library/code"
	pb "github.com/r59q/forge-tools/forge-code/service/codepb"
	"github.com/r59q/forge-tools/forge-core/servicekit"
)

// REST registers generated routes and GET /v1/raw. Authorization is the
// sole forwarded header; Grpc-Metadata-* headers cannot inject
// credentials. Errors never echo input or forge responses. Responses are
// not cached. Bodies are capped at 1 MiB, or 96 MiB for POST /v1/commits.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(false)
	if err := pb.RegisterCodeServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}

	fail := func(w http.ResponseWriter, r *http.Request, err error) {
		_, marshaler := runtime.MarshalerForRequest(mux, r)
		runtime.HTTPError(r.Context(), mux, marshaler, w, r, err)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		limit := int64(maxBodyBytes)
		if r.URL.Path == "/v1/commits" {
			// Check the token before accepting a large body.
			if err := s.check(r.Context(), r.Header); err != nil {
				fail(w, r, err)
				return
			}

			// The upload may take up to the timeout, and the push gets its
			// own timeout once the body is decoded.
			limit = maxCommitBytes
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(s.timeout))
			_ = rc.SetWriteDeadline(time.Now().Add(2*s.timeout + 5*time.Second))
		}

		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if r.URL.Path == "/v1/raw" {
			s.raw(w, r, fail)
			return
		}

		mux.ServeHTTP(w, r)
	}), nil
}

// rawRequest is a parsed GET /v1/raw query.
type rawRequest struct {
	ref      code.RepositoryRef
	revision string
	path     string
	blob     string
}

// parseRaw parses a /v1/raw query: source, repository and either path
// with an optional revision, or blob. Each may appear once.
func parseRaw(query string) (rawRequest, error) {
	bad := fmt.Errorf("%w: raw query", code.ErrInvalid)
	q, err := url.ParseQuery(query)
	if err != nil {
		return rawRequest{}, bad
	}

	var r rawRequest
	fields := map[string]*string{
		"source":     &r.ref.Source,
		"repository": &r.ref.Repository,
		"revision":   &r.revision,
		"path":       &r.path,
		"blob":       &r.blob,
	}
	for key, values := range q {
		field, ok := fields[key]
		if !ok || len(values) != 1 {
			return rawRequest{}, bad
		}

		*field = values[0]
	}

	if (r.blob == "") == (r.path == "") || (r.blob != "" && r.revision != "") {
		return rawRequest{}, bad
	}

	return r, nil
}

// raw serves GET /v1/raw: file or blob content as an attachment, streamed
// within the stream timeout. Errors before the first byte use fail; later
// errors abort the connection so the truncation shows.
func (s *Server) raw(w http.ResponseWriter, r *http.Request, fail func(http.ResponseWriter, *http.Request, error)) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.streamTimeout)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(s.streamTimeout))
	if err := s.check(ctx, r.Header); err != nil {
		fail(w, r, err)
		return
	}

	req, err := parseRaw(r.URL.RawQuery)
	if err != nil {
		fail(w, r, rpcError(err))
		return
	}

	client, release, err := s.begin(ctx, req.ref.Source)
	if err != nil {
		fail(w, r, err)
		return
	}

	defer release()
	var rd *code.Reader
	if req.blob != "" {
		rd, err = client.ReadBlob(ctx, req.ref, code.OID(req.blob))
	} else {
		rd, err = client.ReadFile(ctx, req.ref, req.revision, req.path)
	}

	if err != nil {
		fail(w, r, rpcError(err))
		return
	}

	defer rd.Close()
	buf := make([]byte, 32<<10)
	n, err := fill(rd, buf)
	done := errors.Is(err, io.EOF)
	if err != nil && !done {
		fail(w, r, rpcError(err))
		return
	}

	rawHeaders(w.Header(), rd)
	w.WriteHeader(http.StatusOK)
	sent, err := w.Write(buf[:n])
	total := int64(sent)
	if err == nil && !done {
		var more int64
		more, err = io.CopyBuffer(w, rd, buf)
		total += more
	}

	if err != nil || (rd.Size >= 0 && total != rd.Size) {
		panic(http.ErrAbortHandler)
	}
}

// rawHeaders sets the headers of a /v1/raw response for rd.
func rawHeaders(h http.Header, rd *code.Reader) {
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", "attachment")
	h.Set("X-Forge-Code-Blob", string(rd.ID))
	if rd.Commit != "" {
		h.Set("X-Forge-Code-Commit", string(rd.Commit))
	}

	h.Set("X-Forge-Code-LFS-Pointer", strconv.FormatBool(rd.LFSPointer))
	if rd.Size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(rd.Size, 10))
	}
}
