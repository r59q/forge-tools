package forgehttp

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func response(status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	u, _ := url.Parse("https://user:pass@forge.example/api/v1/user?access_token=secret#frag")
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    &http.Request{Method: http.MethodGet, URL: u},
	}
}

func TestCheckResponse(t *testing.T) {
	jsonType := http.Header{"Content-Type": {"application/json; charset=utf-8"}}
	reset := time.Now().Add(time.Hour).Unix()
	tests := []struct {
		name    string
		status  int
		header  http.Header
		body    string
		want    error // sentinel, or nil for none
		wantMsg string
	}{
		{"ok", 200, nil, "", nil, ""},
		{"no content", 204, nil, "", nil, ""},
		{"not modified", 304, nil, "", nil, ""},
		{"unauthorized", 401, jsonType, `{"message":"Bad credentials"}`, ErrUnauthorized, "Bad credentials"},
		{"gitlab unauthorized", 401, jsonType, `{"message":"401 Unauthorized"}`, ErrUnauthorized, "401 Unauthorized"},
		{"forbidden", 403, jsonType, `{"message":"Resource not accessible"}`, ErrForbidden, "Resource not accessible"},
		{"forbidden quota left", 403, http.Header{"X-Ratelimit-Remaining": {"10"}}, "", ErrForbidden, ""},
		{"forbidden quota exhausted", 403, http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(reset, 10)}}, "", ErrRateLimited, ""},
		{"forbidden retry after", 403, http.Header{"Retry-After": {"60"}}, "", ErrRateLimited, ""},
		{"forbidden secondary limit", 403, jsonType, `{"message":"You have exceeded a secondary rate limit."}`, ErrRateLimited, "You have exceeded a secondary rate limit."},
		{"not found", 404, jsonType, `{"message":"Not Found"}`, ErrNotFound, "Not Found"},
		{"gitlab not found", 404, jsonType, `{"message":"404 Project Not Found"}`, ErrNotFound, "404 Project Not Found"},
		{"conflict", 409, nil, "", ErrConflict, ""},
		{"too many requests", 429, http.Header{"Retry-After": {"5"}}, "", ErrRateLimited, ""},
		{"validation object", 400, jsonType, `{"message":{"title":["can't be blank"]}}`, nil, `{"title":["can't be blank"]}`},
		{"error field", 400, jsonType, `{"error":"invalid_scope"}`, nil, "invalid_scope"},
		{"unprocessable", 422, jsonType, `{"message":"Validation Failed","errors":[]}`, nil, "Validation Failed"},
		{"server error html", 502, http.Header{"Content-Type": {"text/html"}}, "<html>Bad gateway</html>", nil, ""},
		{"plain text", 500, http.Header{"Content-Type": {"text/plain"}}, "  boom\n  again ", nil, "boom again"},
		{"json without type", 500, nil, `{"message":"oops"}`, nil, "oops"},
		{"bad json", 500, jsonType, `{"message":`, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckResponse(response(tt.status, tt.header, tt.body))
			if tt.status < 400 {
				if err != nil {
					t.Fatalf("CheckResponse() = %v, want nil", err)
				}
				return
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("CheckResponse() = %v, want *Error", err)
			}
			if e.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", e.StatusCode, tt.status)
			}
			if e.Err != tt.want {
				t.Errorf("Err = %v, want %v", e.Err, tt.want)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("errors.Is(%v, %v) = false", err, tt.want)
			}
			if e.Message != tt.wantMsg {
				t.Errorf("Message = %q, want %q", e.Message, tt.wantMsg)
			}
			if e.URL != "https://forge.example/api/v1/user" {
				t.Errorf("URL = %q, want it without user info, query and fragment", e.URL)
			}
			for _, secret := range []string{"secret", "pass"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("Error() = %q contains %q", err.Error(), secret)
				}
			}
		})
	}
}

func TestCheckResponseKeepsBody(t *testing.T) {
	body := `{"message":"Not Found","documentation_url":"https://docs"}`
	resp := response(404, http.Header{"Content-Type": {"application/json"}}, body)
	if err := CheckResponse(resp); err == nil {
		t.Fatal("CheckResponse() = nil, want error")
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("body after CheckResponse = %q, want %q", got, body)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
}

func TestCheckResponseLongMessage(t *testing.T) {
	long := strings.Repeat("x", 1000)
	err := CheckResponse(response(500, http.Header{"Content-Type": {"text/plain"}}, long))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("CheckResponse() = %v, want *Error", err)
	}
	if len(e.Message) > maxMessage+3 {
		t.Errorf("len(Message) = %d, want at most %d", len(e.Message), maxMessage+3)
	}
}

func TestErrorString(t *testing.T) {
	e := &Error{StatusCode: 401, Method: "GET", URL: "https://forge.example/user", Message: "Bad credentials", Err: ErrUnauthorized}
	want := "forgehttp: GET https://forge.example/user: 401 Unauthorized: Bad credentials"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got := (&Error{StatusCode: 500}).Error(); got != "forgehttp: 500 Internal Server Error" {
		t.Errorf("Error() = %q", got)
	}
}
