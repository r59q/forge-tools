package evidence

import (
	"errors"
	"strings"
	"testing"
)

// secret marks untrusted input that must never show up in an error.
const secret = "SECRET"

func goldenMetadata(t *testing.T) string {
	t.Helper()

	if golden == "" {
		t.Fatal("testdata/entry.json is empty")
	}
	return golden
}

func TestDecodeGolden(t *testing.T) {
	entry, err := Decode([]byte(goldenMetadata(t)), Limits{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if entry.Name != "unit-tests" || entry.Subject.Path != "forge-evidence/library" || len(entry.Files) != 2 {
		t.Fatalf("Decode = %+v", entry)
	}
}

func TestDecodeRejects(t *testing.T) {
	golden := goldenMetadata(t)
	replace := func(old, replacement string) string {
		if !strings.Contains(golden, old) {
			t.Fatalf("golden metadata has no %q", old)
		}
		return strings.Replace(golden, old, replacement, 1)
	}
	file := `"name": "junit.xml",`
	subject := `"subject": {
    "commit": "` + exampleCommit + `",
    "path": "forge-evidence/library"
  },`
	digest := `,
      "sha256": "` + otherDigest + `"`
	withoutFiles, _, found := strings.Cut(golden, `,
  "files"`)
	if !found {
		t.Fatal("golden metadata has no files")
	}

	cases := []struct {
		name  string
		input string
		want  error
	}{
		{"empty", "", ErrInvalid},
		{"not JSON", secret, ErrInvalid},
		{"truncated", golden[:len(golden)/2], ErrInvalid},
		{"array", `[1]`, ErrInvalid},
		{"null", `null`, ErrInvalid},
		{"trailing data", golden + secret, ErrInvalid},
		{"invalid UTF-8", replace(`"Unit tests pass"`, "\"\xff"+secret+"\""), ErrInvalid},
		{"duplicate field", replace(`"kind": "test-result",`, `"kind": "test-result", "kind": "`+secret+`",`), ErrInvalid},
		{"unknown field", replace(`"version": 1,`, `"version": 1, "`+secret+`": 1,`), ErrInvalid},
		{"unknown file field", replace(file, file+` "`+secret+`": true,`), ErrInvalid},
		{"field name case", replace(`"title"`, `"Title"`), ErrInvalid},
		{"missing version", replace(`"version": 1,`, ``), ErrInvalid},
		{"version as string", replace(`"version": 1,`, `"version": "1",`), ErrInvalid},
		{"version as float", replace(`"version": 1,`, `"version": 1.5,`), ErrInvalid},
		{"unknown version", replace(`"version": 1,`, `"version": 2,`), ErrVersion},
		{"unknown version with new fields", `{"version": 2, "` + secret + `": 1}`, ErrVersion},
		{"zero version", replace(`"version": 1,`, `"version": 0,`), ErrVersion},
		{"missing name", replace(`"name": "unit-tests",`, ``), ErrInvalid},
		{"missing title", replace(`"title": "Unit tests pass",`, ``), ErrInvalid},
		{"null title", replace(`"Unit tests pass"`, `null`), ErrInvalid},
		{"missing kind", replace(`"kind": "test-result",`, ``), ErrInvalid},
		{"missing subject", replace(subject, ``), ErrInvalid},
		{"null subject", replace(subject, `"subject": null,`), ErrInvalid},
		{"missing commit", replace(`"commit": "`+exampleCommit+`",`, ``), ErrInvalid},
		{"missing files", withoutFiles + "}", ErrInvalid},
		{"no files", withoutFiles + `, "files": []}`, ErrInvalid},
		{"missing size", replace(`"size": 2048,`, ``), ErrInvalid},
		{"missing digest", replace(digest, ``), ErrInvalid},
		{"short commit", replace(exampleCommit, "4b825dc"), ErrInvalid},
		{"commit ref", replace(exampleCommit, secret), ErrInvalid},
		{"uppercase commit", replace(exampleCommit, strings.ToUpper(exampleCommit)), ErrInvalid},
		{"absolute path", replace(`"forge-evidence/library"`, `"/`+secret+`"`), ErrInvalid},
		{"traversal path", replace(`"forge-evidence/library"`, `"a/../`+secret+`"`), ErrInvalid},
		{"backslash path", replace(`"forge-evidence/library"`, `"a\\`+secret+`"`), ErrInvalid},
		{"NUL path", replace(`"forge-evidence/library"`, `"a\u0000`+secret+`"`), ErrInvalid},
		{"traversal entry name", replace(`"unit-tests"`, `"../`+secret+`"`), ErrInvalid},
		{"traversal file name", replace(`"junit.xml"`, `"../`+secret+`"`), ErrInvalid},
		{"separator file name", replace(`"junit.xml"`, `"a/`+secret+`"`), ErrInvalid},
		{"metadata file name", replace(`"junit.xml"`, `"Entry.JSON"`), ErrInvalid},
		{"duplicate file name", replace(`"junit.xml"`, `"GO-TEST.log"`), ErrInvalid},
		{"control in title", replace(`"Unit tests pass"`, `"`+secret+`\n"`), ErrInvalid},
		{"invalid kind", replace(`"test-result"`, `"`+secret+`"`), ErrInvalid},
		{"negative size", replace(`"size": 2048,`, `"size": -1,`), ErrInvalid},
		{"size overflow", replace(`"size": 2048,`, `"size": 99999999999999999999,`), ErrInvalid},
		{"invalid digest", replace(otherDigest, secret), ErrInvalid},
		{"uppercase digest", replace(otherDigest, strings.ToUpper(otherDigest)), ErrInvalid},
		{"file over limit", replace(`"size": 2048,`, `"size": 4194305,`), ErrTooLarge},
		{"oversized input", golden + strings.Repeat(" ", 64<<10), ErrTooLarge},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode([]byte(test.input), Limits{})
			if !errors.Is(err, test.want) {
				t.Fatalf("Decode = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("Decode error %q contains input", err)
			}
		})
	}
}

func TestDecodeChecksSizeBeforeParsing(t *testing.T) {
	input := "{" + strings.Repeat(secret, 100)

	_, err := Decode([]byte(input), Limits{MetadataBytes: 64})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Decode = %v, want ErrTooLarge", err)
	}
}

func TestDecodeAppliesEntryLimits(t *testing.T) {
	golden := []byte(goldenMetadata(t))

	limits := []Limits{
		{Files: 1},
		{FileBytes: 1024},
		{EntryBytes: 2047},
	}
	for _, limit := range limits {
		if _, err := Decode(golden, limit); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Decode with %+v = %v, want ErrTooLarge", limit, err)
		}
	}

	if _, err := Decode(golden, Limits{Files: 2, FileBytes: 2048, EntryBytes: 2048}); err != nil {
		t.Errorf("Decode at the limits = %v, want nil", err)
	}
}
