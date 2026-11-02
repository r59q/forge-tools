// Package api exposes repository content over gRPC, generated REST routes
// and a raw download route.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/r59q/forge-tools/forge-code/library/code"
	pb "github.com/r59q/forge-tools/forge-code/service/codepb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/tap"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Size limits.
const (
	// MaxRecvMsgSize is the gRPC receive limit the server needs for
	// CreateCommit: 64 MiB of content plus overhead.
	MaxRecvMsgSize = 72 << 20
	// MaxFileBytes caps the content of GetFile and GetBlob.
	MaxFileBytes = 4 << 20
	// ChunkBytes caps the data of one streamed Chunk.
	ChunkBytes = 64 << 10

	maxBodyBytes   = 1 << 20  // REST request bodies
	maxCommitBytes = 96 << 20 // POST /v1/commits: 64 MiB as base64 JSON
	maxStreams     = 100      // concurrent streams per gRPC connection
)

// Config defaults.
const (
	// DefaultStreamTimeout limits ReadFile, ReadBlob and GET /v1/raw.
	DefaultStreamTimeout = 10 * time.Minute
	// DefaultMaxInFlight is the default per-source in-flight limit.
	DefaultMaxInFlight = 8
)

// ErrInvalid means New was given nil sources or credential source, a
// nonpositive timeout, or a negative stream timeout or in-flight limit.
var ErrInvalid = errors.New("api: invalid configuration")

// errFileTooLarge is a GetFile or GetBlob over MaxFileBytes.
var errFileTooLarge = fmt.Errorf("%w: content over %d bytes; use a stream", code.ErrTooLarge, MaxFileBytes)

// Config configures a Server.
type Config struct {
	// Timeout limits each unary RPC. It is required.
	Timeout time.Duration
	// StreamTimeout limits each stream and raw download. Zero means
	// DefaultStreamTimeout.
	StreamTimeout time.Duration
	// MaxInFlight caps the concurrent requests per source; more are
	// RESOURCE_EXHAUSTED. Zero means DefaultMaxInFlight.
	MaxInFlight int
}

// Server authenticates every RPC and raw download, including direct
// grpc-gateway dispatch. It never logs requests, responses, credentials or
// upstream errors, and returns only a status code to callers.
type Server struct {
	pb.UnimplementedCodeServer
	sources       Sources
	token         forgeauth.Credentials
	timeout       time.Duration
	streamTimeout time.Duration
	inFlight      *limiter
}

// Sources returns a client for the current sources. The server calls it once
// per request, so sources may change while it runs. Errors map to
// Unavailable unless they wrap a context error.
type Sources func(ctx context.Context) (*code.Client, error)

// Fixed returns Sources that always use client.
func Fixed(client *code.Client) Sources {
	return func(context.Context) (*code.Client, error) { return client, nil }
}

// New constructs a service over sources. Callers must present the token from
// token as Authorization: Bearer. Runtime configuration should use
// forgeauth.FileToken or forgeauth.EnvToken.
func New(sources Sources, token forgeauth.Credentials, c Config) (*Server, error) {
	if sources == nil || token == nil || c.Timeout <= 0 || c.StreamTimeout < 0 || c.MaxInFlight < 0 {
		return nil, ErrInvalid
	}
	if c.StreamTimeout == 0 {
		c.StreamTimeout = DefaultStreamTimeout
	}
	if c.MaxInFlight == 0 {
		c.MaxInFlight = DefaultMaxInFlight
	}
	return &Server{
		sources:       sources,
		token:         token,
		timeout:       c.Timeout,
		streamTimeout: c.StreamTimeout,
		inFlight:      &limiter{max: c.MaxInFlight, n: map[string]int{}},
	}, nil
}

// REST registers generated routes and GET /v1/raw. Authorization is the
// sole forwarded header; Grpc-Metadata-* headers cannot inject
// credentials. Errors never echo input or forge responses. Responses are
// not cached. Bodies are capped at 1 MiB, or 96 MiB for POST /v1/commits.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	marshaler := &runtime.JSONPb{UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: false}}
	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, marshaler),
		runtime.WithIncomingHeaderMatcher(func(string) (string, bool) { return "", false }),
		runtime.WithErrorHandler(func(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
			code := status.Code(err)
			if code == codes.Unknown {
				code = codes.Internal
			}
			runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, status.Error(code, code.String()))
		}),
	)
	if err := pb.RegisterCodeHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}
	fail := func(w http.ResponseWriter, r *http.Request, err error) {
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

// check compares a presented bearer token with the service token in
// constant time.
func (s *Server) check(ctx context.Context, h http.Header) error {
	denied := status.Error(codes.Unauthenticated, "authentication required")
	token, ok := forgeauth.AuthorizationToken(h, "Bearer")
	if !ok {
		return denied
	}
	expected, err := s.token.Token(ctx)
	if err != nil || expected == "" {
		return denied
	}
	a, b := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(expected))
	if subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		return denied
	}
	return nil
}

// authorize checks the Authorization metadata of an RPC.
func (s *Server) authorize(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	return s.checkMD(ctx, md)
}

// checkMD checks the Authorization value of gRPC metadata.
func (s *Server) checkMD(ctx context.Context, md metadata.MD) error {
	h := http.Header{}
	for _, value := range md.Get("authorization") {
		h.Add("Authorization", value)
	}
	return s.check(ctx, h)
}

// GRPCOptions returns the options a gRPC server for s needs: the
// MaxRecvMsgSize limit, 100 concurrent streams per connection, and a token
// check that rejects callers before their request message is read. gRPC
// runs that check on the connection goroutine, so the token source must be
// fast. RPCs check the token again.
func (s *Server) GRPCOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(MaxRecvMsgSize),
		grpc.MaxConcurrentStreams(maxStreams),
		grpc.InTapHandle(func(ctx context.Context, info *tap.Info) (context.Context, error) {
			return ctx, s.checkMD(ctx, info.Header)
		}),
	}
}

// limiter counts in-flight requests per source.
type limiter struct {
	mu  sync.Mutex
	max int
	n   map[string]int
}

// acquire takes a slot for source, or returns false if all are in use.
// An empty source takes no slot, since the library rejects it.
func (l *limiter) acquire(source string) (release func(), ok bool) {
	if source == "" {
		return func() {}, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[source] >= l.max {
		return nil, false
	}
	l.n[source]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.n[source]--; l.n[source] == 0 {
				delete(l.n, source)
			}
		})
	}, true
}

// begin takes an in-flight slot for source and returns the current
// client. Callers must authorize first and call release when done.
func (s *Server) begin(ctx context.Context, source string) (client *code.Client, release func(), err error) {
	release, ok := s.inFlight.acquire(source)
	if !ok {
		return nil, nil, status.Error(codes.ResourceExhausted, codes.ResourceExhausted.String())
	}
	client, err = s.sources(ctx)
	if err != nil {
		release()
		if ctx.Err() != nil {
			return nil, nil, rpcError(ctx.Err())
		}
		return nil, nil, status.Error(codes.Unavailable, codes.Unavailable.String())
	}
	return client, release, nil
}

// call runs a unary RPC on source within the request timeout.
func call[T any](s *Server, ctx context.Context, source string, fn func(context.Context, *code.Client) (T, error)) (T, error) {
	return run(s, ctx, source, false, fn)
}

// write is call for an RPC that writes, where a forge 400 or 422 means a
// rejected argument.
func write[T any](s *Server, ctx context.Context, source string, fn func(context.Context, *code.Client) (T, error)) (T, error) {
	return run(s, ctx, source, true, fn)
}

func run[T any](s *Server, ctx context.Context, source string, writes bool, fn func(context.Context, *code.Client) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.authorize(ctx); err != nil {
		return zero, err
	}
	client, release, err := s.begin(ctx, source)
	if err != nil {
		return zero, err
	}
	defer release()
	result, err := fn(ctx, client)
	if err != nil {
		c := codeOf(err, writes)
		return zero, status.Error(c, c.String())
	}
	return result, nil
}

// rpcError maps a read error to a bare status.
func rpcError(err error) error {
	c := codeOf(err, false)
	return status.Error(c, c.String())
}

// codeOf maps library and forge errors to a status code, checked in
// order. A forge rejecting the service's own credentials is Unavailable,
// not Unauthenticated, so callers do not mistake it for a bad service
// token. writes makes a forge 400 or 422 InvalidArgument.
func codeOf(err error, writes bool) codes.Code {
	var forgeErr *forgehttp.Error
	switch {
	case errors.Is(err, context.Canceled):
		return codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded
	case errors.Is(err, code.ErrInvalid), errors.Is(err, code.ErrUnknownSource):
		return codes.InvalidArgument
	case errors.Is(err, code.ErrStale):
		return codes.Aborted
	case errors.Is(err, code.ErrAmbiguous), errors.Is(err, code.ErrNotFile), errors.Is(err, code.ErrRejected):
		return codes.FailedPrecondition
	case errors.Is(err, code.ErrTooLarge):
		return codes.OutOfRange
	case errors.Is(err, code.ErrUnsupported):
		return codes.Unimplemented
	case errors.Is(err, forgehttp.ErrNotFound), errors.Is(err, code.ErrRevisionNotFound), errors.Is(err, code.ErrPathNotFound):
		return codes.NotFound
	case errors.Is(err, forgehttp.ErrForbidden):
		return codes.PermissionDenied
	case errors.Is(err, forgehttp.ErrRateLimited):
		return codes.ResourceExhausted
	case errors.Is(err, forgehttp.ErrConflict):
		return codes.FailedPrecondition
	case writes && errors.As(err, &forgeErr) &&
		(forgeErr.StatusCode == http.StatusBadRequest || forgeErr.StatusCode == http.StatusUnprocessableEntity):
		return codes.InvalidArgument
	}
	return codes.Unavailable
}

// repo returns the repository a request names.
func repo(r interface {
	GetSource() string
	GetRepository() string
}) code.RepositoryRef {
	return code.RepositoryRef{Source: r.GetSource(), Repository: r.GetRepository()}
}

func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func signature(s code.Signature) *pb.Signature {
	return &pb.Signature{Name: s.Name, Email: s.Email, When: timestamp(s.When)}
}

func oids(ids []code.OID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

func commit(c code.Commit) *pb.Commit {
	return &pb.Commit{
		Id:        string(c.ID),
		Tree:      string(c.Tree),
		Parents:   oids(c.Parents),
		Author:    signature(c.Author),
		Committer: signature(c.Committer),
		Message:   c.Message,
	}
}

func entry(e code.Entry) *pb.Entry {
	return &pb.Entry{Name: e.Name, Path: e.Path, Mode: e.Mode.String(), Type: string(e.Type), Id: string(e.ID), Size: e.Size}
}

func blob(b code.Blob) *pb.Blob {
	return &pb.Blob{Id: string(b.ID), Commit: string(b.Commit), Path: b.Path, Size: b.Size, LfsPointer: b.LFSPointer}
}

// mode formats a change mode, "" for none.
func mode(m code.FileMode) string {
	if m == 0 {
		return ""
	}
	return m.String()
}

func change(c code.Change) *pb.Change {
	return &pb.Change{
		Status:       string(c.Status),
		OldPath:      c.OldPath,
		Path:         c.Path,
		OldMode:      mode(c.OldMode),
		Mode:         mode(c.Mode),
		OldId:        string(c.OldID),
		Id:           string(c.ID),
		Binary:       c.Binary,
		Additions:    int32(c.Additions),
		Deletions:    int32(c.Deletions),
		Patch:        c.Patch,
		PatchOmitted: c.PatchOmitted,
	}
}

// readAll reads a whole blob of at most MaxFileBytes and closes rd.
func readAll(rd *code.Reader) (*pb.File, error) {
	defer rd.Close()
	if rd.Size > MaxFileBytes {
		return nil, errFileTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(rd, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes {
		return nil, errFileTooLarge
	}
	b := blob(rd.Blob)
	b.Size = int64(len(data))
	return &pb.File{Blob: b, Content: data}, nil
}

// fromSignature converts a request signature. Nil is the zero Signature.
func fromSignature(s *pb.Signature) (code.Signature, error) {
	if s == nil {
		return code.Signature{}, nil
	}
	out := code.Signature{Name: s.GetName(), Email: s.GetEmail()}
	if s.When != nil {
		if err := s.When.CheckValid(); err != nil {
			return code.Signature{}, fmt.Errorf("%w: signature time", code.ErrInvalid)
		}
		out.When = s.When.AsTime()
	}
	return out, nil
}

func toOIDs(ids []string) []code.OID {
	out := make([]code.OID, len(ids))
	for i, id := range ids {
		out[i] = code.OID(id)
	}
	return out
}

// newCommit converts a CreateCommitRequest. The library checks the
// values.
func newCommit(r *pb.CreateCommitRequest) (code.NewCommit, error) {
	n := code.NewCommit{
		Parents: toOIDs(r.GetParents()),
		Message: r.GetMessage(),
		Ref:     r.GetRef(),
		Old:     code.OID(r.GetOld()),
	}
	var err error
	if n.Author, err = fromSignature(r.GetAuthor()); err != nil {
		return code.NewCommit{}, err
	}
	if n.Committer, err = fromSignature(r.GetCommitter()); err != nil {
		return code.NewCommit{}, err
	}
	for _, c := range r.GetChanges() {
		fc := code.FileChange{Path: c.GetPath(), Delete: c.GetDelete(), Submodule: code.OID(c.GetSubmodule())}
		if c.GetMode() != "" {
			if fc.Mode, err = code.ParseFileMode(c.GetMode()); err != nil {
				return code.NewCommit{}, err
			}
		}
		// Empty content is no content, so it may pair with a delete or a
		// submodule.
		if len(c.GetContent()) > 0 {
			fc.Content = c.GetContent()
		}
		n.Changes = append(n.Changes, fc)
	}
	return n, nil
}

// refUpdates converts the updates of an UpdateRefsRequest.
func refUpdates(r *pb.UpdateRefsRequest) []code.RefUpdate {
	out := make([]code.RefUpdate, len(r.GetUpdates()))
	for i, u := range r.GetUpdates() {
		out[i] = code.RefUpdate{Name: u.GetName(), Old: code.OID(u.GetOld()), New: code.OID(u.GetNew())}
	}
	return out
}

// ListSources lists source IDs and kinds, without URLs or credentials.
func (s *Server) ListSources(ctx context.Context, _ *emptypb.Empty) (*pb.ListSourcesResponse, error) {
	return call(s, ctx, "", func(_ context.Context, client *code.Client) (*pb.ListSourcesResponse, error) {
		result := &pb.ListSourcesResponse{}
		for _, src := range client.Sources() {
			result.Sources = append(result.Sources, &pb.Source{Id: src.ID, Kind: string(src.Session.Kind)})
		}
		return result, nil
	})
}

// GetCapabilities says what a source supports.
func (s *Server) GetCapabilities(ctx context.Context, r *pb.CapabilitiesRequest) (*pb.Capabilities, error) {
	return call(s, ctx, "", func(_ context.Context, client *code.Client) (*pb.Capabilities, error) {
		c, err := client.Capabilities(r.GetSource())
		return &pb.Capabilities{TreeIds: c.TreeIDs, MergeBase: c.MergeBase, Writes: c.Writes}, err
	})
}

// Resolve returns the commit a revision names.
func (s *Server) Resolve(ctx context.Context, r *pb.RevisionRequest) (*pb.Resolved, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.Resolved, error) {
		v, err := client.Resolve(ctx, repo(r), r.GetRevision())
		return &pb.Resolved{Commit: string(v.Commit), Ref: v.Ref}, err
	})
}

// ListRefs lists branches and tags.
func (s *Server) ListRefs(ctx context.Context, r *pb.ListRefsRequest) (*pb.RefList, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.RefList, error) {
		v, err := client.ListRefs(ctx, repo(r), code.RefsOptions{Prefix: r.GetPrefix(), Limit: int(r.GetLimit())})
		out := &pb.RefList{DefaultBranch: v.Default, Truncated: v.Truncated}
		for _, ref := range v.Refs {
			out.Refs = append(out.Refs, &pb.Ref{Name: ref.Name, Target: string(ref.Target), Commit: string(ref.Commit)})
		}
		return out, err
	})
}

// GetCommit reads the commit a revision names.
func (s *Server) GetCommit(ctx context.Context, r *pb.RevisionRequest) (*pb.Commit, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.Commit, error) {
		v, err := client.Commit(ctx, repo(r), r.GetRevision())
		return commit(v), err
	})
}

// ListHistory returns one page of history.
func (s *Server) ListHistory(ctx context.Context, r *pb.HistoryRequest) (*pb.HistoryPage, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.HistoryPage, error) {
		v, err := client.History(ctx, repo(r), code.HistoryOptions{
			Revision: r.GetRevision(),
			Path:     r.GetPath(),
			PageSize: int(r.GetPageSize()),
			Cursor:   r.GetCursor(),
		})
		out := &pb.HistoryPage{Tip: string(v.Tip), Next: v.Next}
		for _, c := range v.Commits {
			out.Commits = append(out.Commits, commit(c))
		}
		return out, err
	})
}

// GetTree lists a directory.
func (s *Server) GetTree(ctx context.Context, r *pb.TreeRequest) (*pb.Tree, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.Tree, error) {
		v, err := client.Tree(ctx, repo(r), r.GetRevision(), code.TreeOptions{
			Path:       r.GetPath(),
			Recursive:  r.GetRecursive(),
			MaxEntries: int(r.GetMaxEntries()),
		})
		out := &pb.Tree{Commit: string(v.Commit), Id: string(v.ID), Path: v.Path, Truncated: v.Truncated}
		for _, e := range v.Entries {
			out.Entries = append(out.Entries, entry(e))
		}
		return out, err
	})
}

// Stat describes one path.
func (s *Server) Stat(ctx context.Context, r *pb.PathRequest) (*pb.PathInfo, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.PathInfo, error) {
		v, err := client.Stat(ctx, repo(r), r.GetRevision(), r.GetPath())
		return &pb.PathInfo{Commit: string(v.Commit), Entry: entry(v.Entry)}, err
	})
}

// GetFile reads a file of at most MaxFileBytes.
func (s *Server) GetFile(ctx context.Context, r *pb.PathRequest) (*pb.File, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.File, error) {
		rd, err := client.ReadFile(ctx, repo(r), r.GetRevision(), r.GetPath())
		if err != nil {
			return nil, err
		}
		return readAll(rd)
	})
}

// GetBlob reads a blob of at most MaxFileBytes.
func (s *Server) GetBlob(ctx context.Context, r *pb.BlobRequest) (*pb.File, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.File, error) {
		rd, err := client.ReadBlob(ctx, repo(r), code.OID(r.GetId()))
		if err != nil {
			return nil, err
		}
		return readAll(rd)
	})
}

// ReadFile streams a file.
func (s *Server) ReadFile(r *pb.PathRequest, stream grpc.ServerStreamingServer[pb.Chunk]) error {
	return s.stream(stream, r.GetSource(), func(ctx context.Context, client *code.Client) (*code.Reader, error) {
		return client.ReadFile(ctx, repo(r), r.GetRevision(), r.GetPath())
	})
}

// ReadBlob streams a blob.
func (s *Server) ReadBlob(r *pb.BlobRequest, stream grpc.ServerStreamingServer[pb.Chunk]) error {
	return s.stream(stream, r.GetSource(), func(ctx context.Context, client *code.Client) (*code.Reader, error) {
		return client.ReadBlob(ctx, repo(r), code.OID(r.GetId()))
	})
}

// stream sends the content open returns in chunks of up to ChunkBytes,
// within the stream timeout. The first chunk carries the blob.
func (s *Server) stream(stream grpc.ServerStreamingServer[pb.Chunk], source string, open func(context.Context, *code.Client) (*code.Reader, error)) error {
	ctx, cancel := context.WithTimeout(stream.Context(), s.streamTimeout)
	defer cancel()
	if err := s.authorize(ctx); err != nil {
		return err
	}
	client, release, err := s.begin(ctx, source)
	if err != nil {
		return err
	}
	// Send waits for a client that stops reading and ignores ctx, so the
	// copy runs apart. Returning ends the stream, which ends a pending
	// Send; the copy then frees the slot.
	done := make(chan error, 1)
	go func() {
		defer release()
		done <- send(ctx, stream, client, open)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return rpcError(ctx.Err())
	}
}

// send streams the content open returns.
func send(ctx context.Context, stream grpc.ServerStreamingServer[pb.Chunk], client *code.Client, open func(context.Context, *code.Client) (*code.Reader, error)) error {
	rd, err := open(ctx, client)
	if err != nil {
		return rpcError(err)
	}
	defer rd.Close()
	buf := make([]byte, ChunkBytes)
	first := &pb.Chunk{Blob: blob(rd.Blob)}
	var total int64
	for {
		n, err := fill(rd, buf)
		if err != nil && err != io.EOF {
			return rpcError(err)
		}
		total += int64(n)
		if err == io.EOF && rd.Size >= 0 && total != rd.Size {
			return status.Error(codes.Unavailable, codes.Unavailable.String())
		}
		if n > 0 || first != nil {
			chunk := first
			if chunk == nil {
				chunk = &pb.Chunk{}
			}
			first = nil
			chunk.Data = buf[:n]
			if err := stream.Send(chunk); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

// fill reads into buf until it is full or r fails. It returns io.EOF only
// at the end of r, unlike io.ReadFull, which also turns an
// io.ErrUnexpectedEOF from r into a normal short read.
func fill(r io.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// GetMergeBase returns the best common ancestor of two revisions.
func (s *Server) GetMergeBase(ctx context.Context, r *pb.MergeBaseRequest) (*pb.MergeBase, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.MergeBase, error) {
		id, err := client.MergeBase(ctx, repo(r), r.GetA(), r.GetB())
		return &pb.MergeBase{Commit: string(id)}, err
	})
}

// Compare lists the changes between two revisions.
func (s *Server) Compare(ctx context.Context, r *pb.CompareRequest) (*pb.Diff, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.Diff, error) {
		v, err := client.Compare(ctx, repo(r), r.GetBase(), r.GetHead(), compareOptions(r))
		out := &pb.Diff{Base: string(v.Base), Head: string(v.Head), MergeBase: string(v.MergeBase), Truncated: v.Truncated}
		for _, c := range v.Changes {
			out.Changes = append(out.Changes, change(c))
		}
		return out, err
	})
}

// compareOptions converts the options of a CompareRequest.
func compareOptions(r *pb.CompareRequest) code.CompareOptions {
	return code.CompareOptions{
		Mode:          code.CompareMode(r.GetMode()),
		Paths:         r.GetPaths(),
		Patches:       r.GetPatches(),
		Context:       int(r.GetContext()),
		MaxChanges:    int(r.GetMaxChanges()),
		MaxPatchFiles: int(r.GetMaxPatchFiles()),
		MaxPatchBytes: int(r.GetMaxPatchBytes()),
	}
}

// CreateCommit makes a commit and moves a ref to it.
func (s *Server) CreateCommit(ctx context.Context, r *pb.CreateCommitRequest) (*pb.Commit, error) {
	return write(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.Commit, error) {
		n, err := newCommit(r)
		if err != nil {
			return nil, err
		}
		v, err := client.CreateCommit(ctx, repo(r), n)
		return commit(v), err
	})
}

// UpdateRefs creates, moves and deletes refs atomically.
func (s *Server) UpdateRefs(ctx context.Context, r *pb.UpdateRefsRequest) (*emptypb.Empty, error) {
	return write(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, client.UpdateRefs(ctx, repo(r), refUpdates(r)...)
	})
}

// rawRequest is a parsed GET /v1/raw query.
type rawRequest struct {
	ref                  code.RepositoryRef
	revision, path, blob string
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
		"source": &r.ref.Source, "repository": &r.ref.Repository,
		"revision": &r.revision, "path": &r.path, "blob": &r.blob,
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
	done := err == io.EOF
	if err != nil && !done {
		fail(w, r, rpcError(err))
		return
	}
	h := w.Header()
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
