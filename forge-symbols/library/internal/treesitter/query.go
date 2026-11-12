package treesitter

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Predicate step types, as in TSQueryPredicateStepType.
const (
	stepDone    = 0
	stepCapture = 1
	stepString  = 2
)

var errMalformed = errors.New("grammar module returned malformed data")

// query is the host's view of a compiled tag query.
type query struct {
	captures []string
	patterns [][]predicate
}

// step is one word of a predicate: its name, a capture or a string.
type step struct {
	kind  uint32
	value string
}

// parseQuery decodes the output of sym_info.
func parseQuery(info []byte) (*query, error) {
	reader := &reader{data: info}
	captures := reader.strings()
	texts := reader.strings()

	query := &query{captures: captures}
	for range reader.count(4) {
		steps := readSteps(reader, captures, texts)

		predicates, err := newPredicates(steps)
		if err != nil {
			return nil, err
		}
		query.patterns = append(query.patterns, predicates)
	}

	if reader.failed {
		return nil, errMalformed
	}
	return query, nil
}

func readSteps(reader *reader, captures, texts []string) []step {
	var steps []step

	for range reader.count(8) {
		kind, id := reader.u32(), reader.u32()

		names := texts
		if kind == stepCapture {
			names = captures
		}

		value := ""
		if kind != stepDone {
			value = reader.pick(names, id)
		}
		steps = append(steps, step{kind: kind, value: value})
	}
	return steps
}

// decode turns the output of sym_analyze into the matches that pass the
// predicates of their pattern.
func (q *query) decode(records, source []byte) ([]Match, error) {
	reader := &reader{data: records}
	matches := []Match{}

	for reader.more() {
		pattern := int(reader.u32())
		captures := q.readCaptures(reader, len(source))

		if reader.failed || pattern >= len(q.patterns) {
			return nil, errMalformed
		}
		if q.accepts(pattern, captures, source) {
			matches = append(matches, Match{Captures: captures})
		}
	}
	return matches, nil
}

func (q *query) readCaptures(reader *reader, sourceBytes int) []Capture {
	count := reader.count(12)
	captures := make([]Capture, 0, count)

	for range count {
		name := reader.pick(q.captures, reader.u32())
		start, end := int(reader.u32()), int(reader.u32())

		if start > end || end > sourceBytes {
			reader.failed = true
		}
		captures = append(captures, Capture{Name: name, Start: start, End: end})
	}
	return captures
}

func (q *query) accepts(pattern int, captures []Capture, source []byte) bool {
	if len(q.patterns[pattern]) == 0 {
		return true
	}

	texts := func(name string) []string {
		var texts []string
		for _, capture := range captures {
			if capture.Name == name {
				texts = append(texts, string(source[capture.Start:capture.End]))
			}
		}
		return texts
	}

	for _, predicate := range q.patterns[pattern] {
		if !predicate(texts) {
			return false
		}
	}
	return true
}

// reader decodes little-endian words and length-prefixed strings. After a
// short read it sets failed and returns zero values.
type reader struct {
	data   []byte
	failed bool
}

func (r *reader) more() bool {
	return !r.failed && len(r.data) > 0
}

func (r *reader) u32() uint32 {
	if len(r.data) < 4 {
		r.failed = true
		return 0
	}

	value := binary.LittleEndian.Uint32(r.data)
	r.data = r.data[4:]
	return value
}

// count reads how many items follow and rejects counts the remaining data
// cannot hold, given the least bytes one item takes.
func (r *reader) count(itemBytes int) int {
	count := int(r.u32())
	if count > len(r.data)/itemBytes {
		r.failed = true
		return 0
	}
	return count
}

func (r *reader) strings() []string {
	count := r.count(4)
	values := make([]string, 0, count)

	for range count {
		length := r.count(1)
		values = append(values, string(r.data[:length]))
		r.data = r.data[length:]
	}
	return values
}

func (r *reader) pick(values []string, index uint32) string {
	if int(index) >= len(values) {
		r.failed = true
		return ""
	}
	return values[index]
}

func (s step) String() string {
	if s.kind == stepCapture {
		return "@" + s.value
	}
	return fmt.Sprintf("%q", s.value)
}
