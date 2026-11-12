package treesitter

import (
	"strings"
	"testing"
)

func word(value string) step {
	return step{kind: stepString, value: value}
}

func captured(name string) step {
	return step{kind: stepCapture, value: name}
}

// texts answers predicate lookups from a fixed set of captures.
func texts(captures map[string][]string) func(string) []string {
	return func(name string) []string { return captures[name] }
}

func TestPredicates(t *testing.T) {
	captures := map[string][]string{
		"name":  {"Open"},
		"same":  {"Open"},
		"other": {"Close"},
		"many":  {"Open", "Close"},
	}

	tests := []struct {
		name      string
		arguments []step
		want      bool
	}{
		{"eq?", []step{captured("name"), word("Open")}, true},
		{"eq?", []step{captured("name"), word("open")}, false},
		{"not-eq?", []step{captured("name"), word("open")}, true},
		{"not-eq?", []step{captured("name"), word("Open")}, false},
		{"eq?", []step{captured("name"), captured("same")}, true},
		{"eq?", []step{captured("name"), captured("other")}, false},
		{"not-eq?", []step{captured("name"), captured("other")}, true},
		{"eq?", []step{captured("many"), word("Open")}, false},
		{"eq?", []step{captured("missing"), word("Open")}, true},
		{"match?", []step{captured("name"), word("^[A-Z]")}, true},
		{"match?", []step{captured("name"), word("^[a-z]")}, false},
		{"not-match?", []step{captured("name"), word("^[a-z]")}, true},
		{"any-of?", []step{captured("name"), word("Close"), word("Open")}, true},
		{"any-of?", []step{captured("name"), word("Close")}, false},
		{"not-any-of?", []step{captured("name"), word("Close")}, true},
		{"not-any-of?", []step{captured("many"), word("Close")}, false},
	}
	for _, test := range tests {
		predicate, err := newPredicate(test.name, test.arguments)
		if err != nil {
			t.Errorf("#%s %v: %v", test.name, test.arguments, err)
			continue
		}
		if got := predicate(texts(captures)); got != test.want {
			t.Errorf("#%s %v = %v, want %v", test.name, test.arguments, got, test.want)
		}
	}
}

func TestDirectivesAreIgnored(t *testing.T) {
	predicate, err := newPredicate("set!", []step{word("injection.language"), word("go")})
	if predicate != nil || err != nil {
		t.Errorf("got %v, %v; want no predicate and no error", predicate != nil, err)
	}
}

func TestUnsupportedPredicates(t *testing.T) {
	tests := []struct {
		name      string
		arguments []step
	}{
		{"is?", []step{captured("name"), word("local")}},
		{"lua-match?", []step{captured("name"), word("^%u")}},
		{"eq?", []step{captured("name")}},
		{"eq?", []step{word("Open"), captured("name")}},
		{"eq?", []step{captured("name"), word("a"), word("b")}},
		{"match?", []step{captured("name"), captured("other")}},
		{"match?", []step{captured("name"), word("(")}},
		{"any-of?", []step{captured("name"), captured("other")}},
	}
	for _, test := range tests {
		_, err := newPredicate(test.name, test.arguments)
		if err == nil || !strings.Contains(err.Error(), "unsupported predicate #"+test.name) {
			t.Errorf("#%s %v: got %v, want an unsupported predicate error", test.name, test.arguments, err)
		}
	}
}

func TestNewPredicatesSplitsSteps(t *testing.T) {
	done := step{kind: stepDone}
	steps := []step{
		word("eq?"), captured("name"), word("Open"), done,
		word("set!"), word("key"), word("value"), done,
		word("not-eq?"), captured("name"), word("Close"), done,
	}

	predicates, err := newPredicates(steps)
	if err != nil || len(predicates) != 2 {
		t.Fatalf("got %d predicates, %v; want 2", len(predicates), err)
	}

	malformed := [][]step{
		{word("eq?"), captured("name"), word("Open")},
		{done},
		{captured("name"), word("Open"), done},
	}
	for _, steps := range malformed {
		if _, err := newPredicates(steps); err == nil {
			t.Errorf("steps %v: want an error", steps)
		}
	}
}
