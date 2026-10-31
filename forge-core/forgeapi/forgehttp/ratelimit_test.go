package forgehttp

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRateLimit(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	reset := time.Unix(1793454000, 0).UTC()
	tests := []struct {
		name   string
		header http.Header
		want   RateLimit
		wantOK bool
	}{
		{"none", http.Header{}, RateLimit{Limit: -1, Remaining: -1}, false},
		{
			"github",
			http.Header{
				"X-Ratelimit-Limit":     {"5000"},
				"X-Ratelimit-Remaining": {"4987"},
				"X-Ratelimit-Reset":     {"1793454000"},
				"X-Ratelimit-Used":      {"13"},
			},
			RateLimit{Limit: 5000, Remaining: 4987, Reset: reset},
			true,
		},
		{
			"gitlab",
			http.Header{
				"Ratelimit-Limit":     {"2000"},
				"Ratelimit-Remaining": {"0"},
				"Ratelimit-Reset":     {"1793454000"},
				"Ratelimit-Resettime": {"Sat, 31 Oct 2026 13:00:00 GMT"},
				"Retry-After":         {"30"},
			},
			RateLimit{Limit: 2000, Remaining: 0, Reset: reset, RetryAfter: 30 * time.Second},
			true,
		},
		{
			"gitlab reset time only",
			http.Header{"Ratelimit-Resettime": {"Sat, 31 Oct 2026 13:00:00 GMT"}},
			RateLimit{Limit: -1, Remaining: -1, Reset: time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC)},
			true,
		},
		{
			"github preferred over gitlab",
			http.Header{"X-Ratelimit-Remaining": {"1"}, "Ratelimit-Remaining": {"2"}, "Ratelimit-Limit": {"10"}},
			RateLimit{Limit: 10, Remaining: 1},
			true,
		},
		{
			"retry after seconds",
			http.Header{"Retry-After": {"120"}},
			RateLimit{Limit: -1, Remaining: -1, RetryAfter: 2 * time.Minute},
			true,
		},
		{
			"retry after date",
			http.Header{"Retry-After": {"Sat, 31 Oct 2026 12:01:30 GMT"}},
			RateLimit{Limit: -1, Remaining: -1, RetryAfter: 90 * time.Second},
			true,
		},
		{
			"retry after past date",
			http.Header{"Retry-After": {"Sat, 31 Oct 2026 11:00:00 GMT"}},
			RateLimit{Limit: -1, Remaining: -1},
			true,
		},
		{
			"invalid values ignored",
			http.Header{"X-Ratelimit-Limit": {"lots"}, "X-Ratelimit-Remaining": {"-3"}, "Retry-After": {"soon"}},
			RateLimit{Limit: -1, Remaining: -1},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRateLimit(tt.header, now)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got.Limit != tt.want.Limit || got.Remaining != tt.want.Remaining ||
				!got.Reset.Equal(tt.want.Reset) || got.RetryAfter != tt.want.RetryAfter {
				t.Errorf("parseRateLimit() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRateLimitExhausted(t *testing.T) {
	tests := []struct {
		remaining int
		want      bool
	}{{-1, false}, {0, true}, {1, false}}
	for _, tt := range tests {
		if got := (RateLimit{Remaining: tt.remaining}).Exhausted(); got != tt.want {
			t.Errorf("Exhausted() with Remaining %d = %v, want %v", tt.remaining, got, tt.want)
		}
	}
}

func TestParseRateLimitUsesNow(t *testing.T) {
	h := http.Header{"Retry-After": {time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)}}
	rl, ok := ParseRateLimit(h)
	if !ok || rl.RetryAfter < 59*time.Minute || rl.RetryAfter > time.Hour {
		t.Errorf("ParseRateLimit() = %+v, %v, want about one hour", rl, ok)
	}
}
