package search

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// read returns the ID and content of each document Read visits.
func read(t *testing.T, engine *Engine, text string, filter Filter) []string {
	t.Helper()

	visited := []string{}
	err := engine.Read(context.Background(), testIndex, text, filter, func(document Document) bool {
		visited = append(visited, document.ID+"="+string(document.Content))
		return true
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return visited
}

func TestReadNarrowsByText(t *testing.T) {
	engine, _ := newIndex(t, IndexLimits{},
		code("c", "needle"),
		code("a", "need\nle"),
		code("b", "haystack"),
		issue("d", "needle"),
		code("e", "bin\x00needle"),
	)

	tests := []struct {
		text   string
		filter Filter
		want   []string
	}{
		{"needle", Filter{}, []string{"c=needle", "d=needle"}},
		{"needle", Filter{Kind: KindCode}, []string{"c=needle"}},
		{"eed", Filter{Kind: KindCode}, []string{"a=need\nle", "c=needle"}},
		{"ne", Filter{Kind: KindCode}, []string{"a=need\nle", "b=haystack", "c=needle", "e="}},
		{"", Filter{IDPrefix: "b"}, []string{"b=haystack"}},
		{"missing", Filter{}, []string{}},
	}
	for _, test := range tests {
		got := read(t, engine, test.text, test.filter)
		if !slices.Equal(got, test.want) {
			t.Errorf("Read(%q, %+v) = %q, want %q", test.text, test.filter, got, test.want)
		}
	}
}

func TestReadStopsWhenVisitReturnsFalse(t *testing.T) {
	engine, _ := newIndex(t, IndexLimits{}, code("a", "x"), code("b", "x"))

	visited := 0
	err := engine.Read(context.Background(), testIndex, "", Filter{}, func(Document) bool {
		visited++
		return false
	})
	if err != nil || visited != 1 {
		t.Errorf("Read visited %d documents, %v, want 1", visited, err)
	}
}

func TestReadRejectsInvalidText(t *testing.T) {
	engine := NewEngine(untouched{t: t}, IndexLimits{})
	visit := func(Document) bool { return true }

	for _, text := range []string{strings.Repeat("a", MaxQueryBytes+1), "bad\xff"} {
		wantError(t, engine.Read(context.Background(), testIndex, text, Filter{}, visit), ErrInvalid)
	}
}
