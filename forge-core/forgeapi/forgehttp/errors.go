// Package forgehttp holds HTTP helpers shared by forge clients: error
// mapping, Link header pagination and rate-limit parsing.
//
// It does not know about credentials and must not import forgeauth.
package forgehttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// Sentinel errors wrapped by *Error. Test for them with errors.Is.
var (
	// ErrUnauthorized means the forge rejected the credentials (HTTP 401).
	ErrUnauthorized = errors.New("forgehttp: unauthorized")
	// ErrForbidden means the credentials lack access (HTTP 403).
	ErrForbidden = errors.New("forgehttp: forbidden")
	// ErrNotFound means the resource does not exist or is hidden (HTTP 404).
	ErrNotFound = errors.New("forgehttp: not found")
	// ErrConflict means the request conflicts with the current state (HTTP 409).
	ErrConflict = errors.New("forgehttp: conflict")
	// ErrRateLimited means a rate limit was hit (HTTP 429, or 403 with
	// rate-limit signals as GitHub sends).
	ErrRateLimited = errors.New("forgehttp: rate limited")
)

// maxErrorBody caps how much of an error body CheckResponse reads.
const maxErrorBody = 64 << 10

// maxMessage caps the length of Error.Message.
const maxMessage = 300

// Error is a failed forge response.
type Error struct {
	// StatusCode is the HTTP status code.
	StatusCode int
	// Method is the request method.
	Method string
	// URL is the request URL without query, fragment or user info.
	URL string
	// Message is the forge's error message, if it sent one.
	Message string
	// RateLimit holds rate-limit headers from the response, or nil.
	RateLimit *RateLimit
	// Err is the sentinel error for the status, or nil if none applies.
	Err error
}

// Error returns a one-line description of the failure.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("forgehttp: ")
	if e.Method != "" {
		b.WriteString(e.Method)
		b.WriteByte(' ')
	}
	if e.URL != "" {
		b.WriteString(e.URL)
		b.WriteString(": ")
	}
	fmt.Fprintf(&b, "%d %s", e.StatusCode, http.StatusText(e.StatusCode))
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// Unwrap returns the sentinel error, so errors.Is works.
func (e *Error) Unwrap() error { return e.Err }

// CheckResponse returns nil if resp has a status below 400. Otherwise it
// returns an *Error with the forge's message and the matching sentinel.
//
// CheckResponse reads part of the body to find the message, but leaves
// resp.Body readable from the start. The caller still closes it.
func CheckResponse(resp *http.Response) error {
	if resp.StatusCode < 400 {
		return nil
	}
	e := &Error{StatusCode: resp.StatusCode}
	if resp.Request != nil {
		e.Method = resp.Request.Method
		if resp.Request.URL != nil {
			e.URL = RedactURL(resp.Request.URL)
		}
	}
	if rl, ok := ParseRateLimit(resp.Header); ok {
		e.RateLimit = &rl
	}
	if resp.Body != nil {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(data), resp.Body), resp.Body}
		e.Message = errorMessage(resp.Header.Get("Content-Type"), data)
	}
	e.Err = sentinel(e)
	return e
}

func sentinel(e *Error) error {
	switch e.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		if isRateLimited(e) {
			return ErrRateLimited
		}
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusConflict:
		return ErrConflict
	case http.StatusTooManyRequests:
		return ErrRateLimited
	}
	return nil
}

// isRateLimited reports whether a 403 is really a rate limit. GitHub sends
// 403 with no remaining quota, a Retry-After header, or a message about
// secondary rate limits.
func isRateLimited(e *Error) bool {
	if rl := e.RateLimit; rl != nil && (rl.Remaining == 0 || rl.RetryAfter > 0) {
		return true
	}
	return strings.Contains(strings.ToLower(e.Message), "rate limit")
}

// errorMessage extracts a short message from an error body. It understands
// JSON bodies with a "message" or "error" field, and plain text.
func errorMessage(contentType string, body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}
	mt, _, _ := mime.ParseMediaType(contentType)
	var msg string
	switch {
	case mt == "application/json" || strings.HasSuffix(mt, "+json") || (mt == "" && body[0] == '{'):
		var v struct {
			Message json.RawMessage `json:"message"`
			Error   json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &v) != nil {
			return ""
		}
		msg = rawString(v.Message)
		if msg == "" {
			msg = rawString(v.Error)
		}
	case mt == "text/plain":
		msg = string(body)
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > maxMessage {
		msg = msg[:maxMessage] + "..."
	}
	return msg
}

// rawString returns a JSON string's value, or other JSON as compact text.
// GitLab sends validation errors as objects.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return ""
	}
	return b.String()
}

// RedactURL returns u without user info, query or fragment, so it is safe
// to put in errors and logs.
func RedactURL(u *url.URL) string {
	r := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawPath: u.RawPath, Opaque: u.Opaque}
	return r.String()
}
