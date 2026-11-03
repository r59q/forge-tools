package changes

import (
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

func TestValidState(t *testing.T) {
	cases := []struct {
		kind  forgeauth.Kind
		state State
		want  bool
	}{
		{forgeauth.GitHub, Open, true},
		{forgeauth.Forgejo, Merged, true},
		{forgeauth.GitLab, Closed, true},
		{forgeauth.GitLab, Locked, true},
		{forgeauth.GitHub, Locked, false},
		{forgeauth.GitLab, "opened", false},
	}

	for _, tc := range cases {
		if got := validState(tc.kind, tc.state); got != tc.want {
			t.Errorf("validState(%s, %s) = %v, want %v", tc.kind, tc.state, got, tc.want)
		}
	}
}

func TestListState(t *testing.T) {
	for _, state := range []string{"all", "open", "closed", "merged"} {
		if !listState(state) {
			t.Errorf("listState(%q) = false", state)
		}
	}

	for _, state := range []string{"", "opened", "locked", "OPEN"} {
		if listState(state) {
			t.Errorf("listState(%q) = true", state)
		}
	}
}

func TestCompleteness(t *testing.T) {
	two := make([]ChangedFile, 2)
	cases := []struct {
		kind     forgeauth.Kind
		items    []ChangedFile
		reported *int
		want     string
	}{
		{forgeauth.GitLab, two, nil, "unknown"},
		{forgeauth.GitHub, two, ptr(2), "complete"},
		{forgeauth.Forgejo, two, ptr(3), "truncated"},
		{forgeauth.GitHub, make([]ChangedFile, 3000), ptr(3000), "truncated"},
	}

	for _, tc := range cases {
		if got := completeness(tc.kind, tc.items, tc.reported); got != tc.want {
			t.Errorf("completeness(%s, %d files) = %q, want %q", tc.kind, len(tc.items), got, tc.want)
		}
	}
}

func TestLowerHex(t *testing.T) {
	for _, v := range "0123456789abcdef" {
		if !lowerHex(v) {
			t.Errorf("lowerHex(%q) = false", v)
		}
	}

	for _, v := range "gA/:`F" {
		if lowerHex(v) {
			t.Errorf("lowerHex(%q) = true", v)
		}
	}
}
