// Package gitwire speaks the client side of Git's smart HTTP protocol: the
// receive-pack advertisement and push with report-status, and a protocol
// v2 upload-pack fetch of one commit. It also writes and reads packs
// without deltas, and has the server-side pieces a fake forge needs.
//
// All parsers treat server output as untrusted and cap lengths and sizes.
// Only SHA-1 repositories are supported.
package gitwire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// Sentinel errors. Errors from HTTP status codes are *forgehttp.Error
// instead.
var (
	// ErrProtocol means the peer sent malformed or unexpected data.
	ErrProtocol = errors.New("gitwire: protocol error")
	// ErrUnsupported means the server lacks a needed feature, such as
	// protocol v2 or filters, or uses SHA-256 objects, or a pack has deltas.
	ErrUnsupported = errors.New("gitwire: not supported")
	// ErrRemote means the server reported an error in the stream.
	ErrRemote = errors.New("gitwire: remote error")
	// ErrTooLarge means a response exceeded a size cap.
	ErrTooLarge = errors.New("gitwire: size limit exceeded")
)

// Media types of the smart HTTP protocol.
const (
	receivePackAdvertisement = "application/x-git-receive-pack-advertisement"
	receivePackRequest       = "application/x-git-receive-pack-request"
	receivePackResult        = "application/x-git-receive-pack-result"
	uploadPackAdvertisement  = "application/x-git-upload-pack-advertisement"
	uploadPackRequest        = "application/x-git-upload-pack-request"
	uploadPackResult         = "application/x-git-upload-pack-result"
)

// maxMessage caps server text kept in errors.
const maxMessage = 300

// endpoint returns repoURL joined with path. repoURL must be an absolute
// http or https URL without user info, query or fragment.
func endpoint(repoURL, path string) (*url.URL, error) {
	u, err := url.Parse(repoURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("gitwire: invalid repository URL")
	}
	return u.JoinPath(path), nil
}

// do sends req with hc and checks the status and response media type. On
// success the caller closes the body.
func do(hc *http.Client, req *http.Request, mediaType string) (*http.Response, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if err := forgehttp.CheckResponse(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != mediaType {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s %s: content type %q, want %s",
			ErrProtocol, req.Method, forgehttp.RedactURL(req.URL), clean([]byte(mt)), mediaType)
	}
	return resp, nil
}

// newRequest builds a request that never gets cached.
func newRequest(ctx context.Context, method string, u *url.URL, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	return req, nil
}

// clean returns server text fit for an error message: control characters
// replaced and cut to maxMessage bytes.
func clean(b []byte) string {
	s := strings.TrimSpace(strings.ToValidUTF8(string(b), "?"))
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > maxMessage {
		s = strings.ToValidUTF8(s[:maxMessage], "") + "..."
	}
	return s
}

// remoteError returns an ErrRemote error with the server's message.
func remoteError(msg []byte) error {
	return fmt.Errorf("%w: %s", ErrRemote, clean(msg))
}

// protoErr returns an ErrProtocol error.
func protoErr(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrProtocol}, args...)...)
}
