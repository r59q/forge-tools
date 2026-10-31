package forgehttp

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimit is rate-limit information from response headers.
//
// It reads Retry-After, GitHub's X-RateLimit-* headers and GitLab's
// RateLimit-* headers.
type RateLimit struct {
	// Limit is the number of requests allowed per window, or -1 if unknown.
	Limit int
	// Remaining is the number of requests left in the window, or -1 if
	// unknown.
	Remaining int
	// Reset is when the window resets, or the zero time if unknown.
	Reset time.Time
	// RetryAfter is how long to wait before retrying, from Retry-After, or
	// 0 if not given.
	RetryAfter time.Duration
}

// Exhausted reports whether no requests are left in the window.
func (r RateLimit) Exhausted() bool { return r.Remaining == 0 }

// ParseRateLimit reads rate-limit headers from h. It returns false if h has
// none.
func ParseRateLimit(h http.Header) (RateLimit, bool) {
	return parseRateLimit(h, time.Now())
}

func parseRateLimit(h http.Header, now time.Time) (RateLimit, bool) {
	rl := RateLimit{Limit: -1, Remaining: -1}
	found := false

	// GitHub uses the X- prefix; GitLab does not. Prefer GitHub's when both
	// are present, as they are on some proxies.
	for _, prefix := range []string{"X-RateLimit-", "RateLimit-"} {
		if rl.Limit < 0 {
			if n, ok := headerInt(h, prefix+"Limit"); ok {
				rl.Limit, found = n, true
			}
		}
		if rl.Remaining < 0 {
			if n, ok := headerInt(h, prefix+"Remaining"); ok {
				rl.Remaining, found = n, true
			}
		}
		if rl.Reset.IsZero() {
			if n, ok := headerInt(h, prefix+"Reset"); ok {
				rl.Reset, found = time.Unix(int64(n), 0).UTC(), true
			}
		}
	}
	// GitLab also sends the reset time as an HTTP date.
	if rl.Reset.IsZero() {
		if t, err := http.ParseTime(h.Get("RateLimit-ResetTime")); err == nil {
			rl.Reset, found = t.UTC(), true
		}
	}

	if d, ok := retryAfter(h.Get("Retry-After"), now); ok {
		rl.RetryAfter, found = d, true
	}
	return rl, found
}

// retryAfter parses a Retry-After value: seconds or an HTTP date.
func retryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	return max(t.Sub(now), 0), true
}

func headerInt(h http.Header, key string) (int, bool) {
	v := strings.TrimSpace(h.Get(key))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
