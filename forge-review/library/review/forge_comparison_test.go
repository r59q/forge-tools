package review

import (
	"context"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"net/http"
	"strings"
	"testing"
)

func TestForgejoComparisonDeduplicatesPaths(t *testing.T) {
	duplicate := true
	fetches := map[string]int{}
	c := connected(t, forgeauth.Forgejo, func(r *http.Request) string {
		switch {
		case strings.Contains(r.URL.Path, "/commits/"):
			sha := baseSHA
			if strings.HasSuffix(r.URL.Path, "/topic") {
				sha = headSHA
			}
			return `{"sha":"` + sha + `"}`
		case strings.Contains(r.URL.Path, "/compare/"):
			if duplicate {
				return `{"files":[{"filename":"a"},{"filename":"b"},{"filename":"a"},{"filename":"b"}]}`
			}
			return `{"files":[{"filename":"a"},{"filename":"b"}]}`
		case strings.Contains(r.URL.Path, "/raw/"):
			fetches[r.URL.Path+"?"+r.URL.RawQuery]++
			if r.URL.Query().Get("ref") == baseSHA {
				return "old\n"
			}
			if r.URL.Query().Get("ref") != headSHA {
				t.Fatal("unpinned content", r.URL)
			}
			return "new\n"
		default:
			t.Fatal("unexpected route", r.URL)
			return ""
		}
	})
	ctx := context.Background()
	in := Input{Kind: "comparison", Ref: Ref{Source: "source", Repository: "o/r"}, From: "main", To: "topic"}
	s, err := c.CreateSubject(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 2 || len(s.Context) != 4 || strings.Count(s.Patch, "@@") != 4 {
		t.Fatalf("duplicate snapshot content: %+v", s)
	}
	if len(fetches) != 4 {
		t.Fatalf("file requests: %v", fetches)
	}
	for path, count := range fetches {
		if count != 1 {
			t.Errorf("fetched %s %d times", path, count)
		}
	}
	for _, path := range []string{"a", "b"} {
		for _, side := range []string{"old", "new"} {
			if err := ValidateAnchor(s, &Anchor{Path: path, Side: side, Start: 1, End: 1}); err != nil {
				t.Fatal(err)
			}
		}
	}
	duplicate = false
	unique, err := c.CreateSubject(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != unique.ID {
		t.Fatal("duplicate entries changed snapshot identity")
	}
}
