package treesitter

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// predicate reports whether a match passes one filter of its pattern. texts
// returns the source text of every node captured under a name.
type predicate func(texts func(capture string) []string) bool

// newPredicates compiles the predicates of one pattern. Steps hold each
// predicate as its name and arguments, ended by a done step.
func newPredicates(steps []step) ([]predicate, error) {
	var predicates []predicate

	for len(steps) > 0 {
		end := slices.IndexFunc(steps, func(s step) bool { return s.kind == stepDone })
		if end < 1 || steps[0].kind != stepString {
			return nil, errMalformed
		}

		predicate, err := newPredicate(steps[0].value, steps[1:end])
		if err != nil {
			return nil, err
		}
		if predicate != nil {
			predicates = append(predicates, predicate)
		}
		steps = steps[end+1:]
	}
	return predicates, nil
}

// newPredicate supports #eq?, #match? and #any-of? with their not- forms.
// Directives, which end in "!", do not filter and are ignored. Anything else
// is an error, so a query never silently matches more than it says.
func newPredicate(name string, arguments []step) (predicate, error) {
	if strings.HasSuffix(name, "!") {
		return nil, nil
	}

	usage := fmt.Errorf("unsupported predicate #%s %v", name, arguments)
	if len(arguments) < 2 || arguments[0].kind != stepCapture {
		return nil, usage
	}

	subject, rest := arguments[0].value, arguments[1:]
	wanted := !strings.HasPrefix(name, "not-")

	switch strings.TrimPrefix(name, "not-") {
	case "eq?":
		return equal(subject, rest, wanted, usage)
	case "match?":
		return matching(subject, rest, wanted, usage)
	case "any-of?":
		return anyOf(subject, rest, wanted, usage)
	}
	return nil, usage
}

// equal compares a capture with a string or, node by node, another capture.
func equal(subject string, rest []step, wanted bool, usage error) (predicate, error) {
	if len(rest) != 1 {
		return nil, usage
	}
	other := rest[0]

	if other.kind == stepString {
		return every(subject, wanted, func(text string) bool { return text == other.value }), nil
	}

	return func(texts func(string) []string) bool {
		left, right := texts(subject), texts(other.value)
		for i := range min(len(left), len(right)) {
			if (left[i] == right[i]) != wanted {
				return false
			}
		}
		return true
	}, nil
}

// matching tests a capture against a regular expression in Go syntax.
func matching(subject string, rest []step, wanted bool, usage error) (predicate, error) {
	if len(rest) != 1 || rest[0].kind != stepString {
		return nil, usage
	}

	pattern, err := regexp.Compile(rest[0].value)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", usage, err)
	}
	return every(subject, wanted, pattern.MatchString), nil
}

func anyOf(subject string, rest []step, wanted bool, usage error) (predicate, error) {
	values := make([]string, 0, len(rest))
	for _, argument := range rest {
		if argument.kind != stepString {
			return nil, usage
		}
		values = append(values, argument.value)
	}

	return every(subject, wanted, func(text string) bool { return slices.Contains(values, text) }), nil
}

// every requires test to give the wanted answer for each node of a capture.
func every(subject string, wanted bool, test func(string) bool) predicate {
	return func(texts func(string) []string) bool {
		for _, text := range texts(subject) {
			if test(text) != wanted {
				return false
			}
		}
		return true
	}
}
