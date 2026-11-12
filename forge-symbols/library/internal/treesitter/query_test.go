package treesitter

import (
	"encoding/binary"
	"errors"
	"testing"
)

// words encodes little-endian 32-bit values and raw strings, as the shim does.
func words(values ...any) []byte {
	var data []byte
	for _, value := range values {
		switch value := value.(type) {
		case int:
			data = binary.LittleEndian.AppendUint32(data, uint32(value))
		case string:
			data = binary.LittleEndian.AppendUint32(data, uint32(len(value)))
			data = append(data, value...)
		}
	}
	return data
}

// info describes a query with captures @name and @definition.function and
// one pattern with the predicate (#eq? @name "main").
func info() []byte {
	return words(
		2, "name", "definition.function",
		2, "eq?", "main",
		1,
		4, stepString, 0, stepCapture, 0, stepString, 1, stepDone, 0,
	)
}

func TestParseQueryAndDecode(t *testing.T) {
	query, err := parseQuery(info())
	if err != nil {
		t.Fatal(err)
	}

	source := []byte("func main() {}; func other() {}")
	records := words(
		0, 2, 0, 5, 9, 1, 0, 14,
		0, 2, 0, 21, 26, 1, 16, 31,
	)

	matches, err := query.decode(records, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want only the one named main", len(matches))
	}

	want := []Capture{{Name: "name", Start: 5, End: 9}, {Name: "definition.function", Start: 0, End: 14}}
	for index, capture := range matches[0].Captures {
		if capture != want[index] {
			t.Errorf("capture %d = %+v, want %+v", index, capture, want[index])
		}
	}

	if matches, err := query.decode(nil, source); err != nil || matches == nil || len(matches) != 0 {
		t.Errorf("no records: got %v, %v; want an empty slice", matches, err)
	}
}

// The host must not trust module output: any inconsistent data is an error,
// never a panic or an out-of-range slice.
func TestMalformedModuleData(t *testing.T) {
	valid := info()
	for size := range len(valid) {
		if _, err := parseQuery(valid[:size]); err == nil && size > 0 {
			t.Errorf("query info truncated to %d bytes: want an error", size)
		}
	}

	queries := map[string][]byte{
		"capture count over data": words(1<<30, "name"),
		"string over data":        words(1, 1<<30),
		"unknown string id":       words(1, "name", 1, "eq?", 1, 2, stepString, 7, stepDone, 0),
		"unknown capture id":      words(1, "name", 1, "eq?", 1, 3, stepString, 0, stepCapture, 9, stepDone, 0),
		"unsupported predicate":   words(1, "name", 1, "is?", 1, 3, stepString, 0, stepCapture, 0, stepDone, 0),
	}
	for name, data := range queries {
		if _, err := parseQuery(data); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}

	query, err := parseQuery(info())
	if err != nil {
		t.Fatal(err)
	}

	source := []byte("func main() {}")
	records := map[string][]byte{
		"unknown pattern":     words(3, 0),
		"unknown capture":     words(0, 1, 5, 0, 4),
		"end before start":    words(0, 1, 0, 9, 5),
		"range beyond source": words(0, 1, 0, 5, 15),
		"capture count":       words(0, 1<<30, 0, 5, 9),
		"truncated":           words(0, 1, 0, 5),
	}
	for name, data := range records {
		if _, err := query.decode(data, source); !errors.Is(err, errMalformed) {
			t.Errorf("%s: got %v, want errMalformed", name, err)
		}
	}
}
