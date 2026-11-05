package yamlnode

import (
	"errors"
	"strings"
	"testing"
)

func TestPairsMergeKeys(t *testing.T) {
	pairs, err := Load([]byte(`
base: &base {a: 1, b: 2}
more: &more {a: lost, c: 3}
job:
  <<: [*base, *more]
  b: override
  d: 4
`))
	if err != nil {
		t.Fatal(err)
	}

	job, ok := Pairs(Get(pairs, "job"))
	if !ok {
		t.Fatal("job is not a mapping")
	}

	var keys []string
	for _, p := range job {
		value, _ := AsString(p.Value)
		keys = append(keys, p.Key+"="+value)
	}

	if got := strings.Join(keys, ","); got != "a=1,b=override,c=3,d=4" {
		t.Fatalf("merged %s", got)
	}
}

func TestLoadErrors(t *testing.T) {
	for _, input := range []string{"a: [", "- a\n- b"} {
		if _, err := Load([]byte(input)); !errors.Is(err, ErrSyntax) {
			t.Errorf("Load(%q) = %v", input, err)
		}
	}
}

func TestReaders(t *testing.T) {
	pairs, _ := Load([]byte(`
s: x
null:
list: [a, [b, c]]
map: {k: 1}
flag: true
n: 7
`))

	if _, ok := AsString(Get(pairs, "null")); ok || !IsNull(Get(pairs, "null")) {
		t.Fatal("null read as string")
	}

	if _, ok := AsStrings(Get(pairs, "list")); ok {
		t.Fatal("nested list read as flat strings")
	}

	if flat, ok := AsFlatStrings(Get(pairs, "list")); !ok || len(flat) != 3 {
		t.Fatalf("flat %v", flat)
	}

	m, ok := AsStringMap(Get(pairs, "map"))
	b, _ := AsBool(Get(pairs, "flag"))
	n, _ := AsInt(Get(pairs, "n"))
	if !ok || m["k"] != "1" || !b || n != 7 {
		t.Fatal("scalar readers")
	}
}

func TestFieldsRest(t *testing.T) {
	pairs, _ := Load([]byte("a: x\nb: [1]\nc: {d: e}\n"))
	f := NewFields(pairs)
	if f.TakeString("a") != "x" || f.TakeString("b") != "" || f.TakeString("missing") != "" {
		t.Fatal("TakeString")
	}

	rest := f.Rest()
	if len(rest) != 2 || rest["b"] == nil || rest["c"] == nil {
		t.Fatalf("rest %v", rest)
	}
}

func TestWrite(t *testing.T) {
	m := NewMap()
	m.Add("jobs", Str("last"))
	m.Add("on", Null())
	m.Add("name", Str("x"))
	m.Add("skipped", nil)
	m.Add("env", StringMap(map[string]string{"N": "0", "B": "true", "S": "1.10", "Z": "007"}))
	m.Add("list", Strings([]string{"one"}))
	skipped := m.AddRest(map[string]any{"name": "dup", "extra": nil})
	m.Sort([]string{"name", "on", "*", "jobs"})

	out, err := Encode(m.Node)
	if err != nil {
		t.Fatal(err)
	}

	want := `name: x
on:
env:
  B: true
  N: 0
  S: "1.10"
  Z: "007"
list: one
extra:
jobs: last
`
	if string(out) != want || len(skipped) != 1 {
		t.Fatalf("got\n%s\nskipped %v", out, skipped)
	}
}

func TestCustomTags(t *testing.T) {
	pairs, _ := Load([]byte("job:\n  script:\n    - !reference [.setup, script]\n    - make\n"))
	job, _ := Pairs(Get(pairs, "job"))
	if _, ok := AsFlatStrings(Get(job, "script")); ok {
		t.Fatal("read a tagged node as commands")
	}

	if tags := CustomTags(pairs); len(tags) != 1 || tags[0] != "!reference on line 3" {
		t.Fatalf("tags %v", tags)
	}
}
