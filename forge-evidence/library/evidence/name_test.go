package evidence

import (
	"errors"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	valid := []string{
		"a",
		"unit-tests",
		"report.xml",
		"Login_Mobile.png",
		"2026-11-16.log",
		"a.b.c",
		"console",
		"com10",
		"nul-check",
		strings.Repeat("a", MaxNameBytes),
	}
	for _, name := range valid {
		if err := ValidName(name); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", name, err)
		}
	}

	invalidNames := []string{
		"",
		".",
		"..",
		"a/../b",
		"../a",
		"a..b",
		"/abs",
		"a/b",
		`a\b`,
		`\abs`,
		`C:\abs`,
		"a\x00b",
		"a\nb",
		"a\tb",
		"a\x7fb",
		".hidden",
		"-flag",
		"_a",
		"a.",
		"a b",
		"a:b",
		"é",
		"con",
		"NUL.txt",
		"Com1.log",
		"lpt9",
		strings.Repeat("a", MaxNameBytes+1),
	}
	for _, name := range invalidNames {
		if err := ValidName(name); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidName(%q) = %v, want ErrInvalid", name, err)
		}
	}
}

func TestValidNameErrorOmitsName(t *testing.T) {
	err := ValidName("SECRET/../x")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("ValidName error = %v, want one without the name", err)
	}
}

func TestValidCommit(t *testing.T) {
	valid := []string{strings.Repeat("a", SHA1Length), strings.Repeat("0123456789abcdef", 4)}
	for _, commit := range valid {
		if err := ValidCommit(commit); err != nil {
			t.Errorf("ValidCommit(%q) = %v, want nil", commit, err)
		}
	}

	invalidCommits := []string{
		"",
		"abc1234",
		strings.Repeat("a", SHA1Length-1),
		strings.Repeat("a", SHA1Length+1),
		strings.Repeat("A", SHA1Length),
		strings.Repeat("g", SHA1Length),
		strings.Repeat("a", SHA256Length+1),
		strings.Repeat("a", SHA1Length-1) + "\x00",
	}
	for _, commit := range invalidCommits {
		if err := ValidCommit(commit); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidCommit(%q) = %v, want ErrInvalid", commit, err)
		}
	}
}

func TestValidPath(t *testing.T) {
	valid := []string{
		"a",
		"forge-evidence/library",
		"src/main.go",
		"docs/my file.md",
		"ünïcode/路径",
		".github/workflows/ci.yml",
		"a..b/c",
		strings.Repeat("a", MaxPathBytes),
	}
	for _, path := range valid {
		if err := ValidPath(path); err != nil {
			t.Errorf("ValidPath(%q) = %v, want nil", path, err)
		}
	}

	invalidPaths := []string{
		"",
		"/abs",
		"a/",
		"a//b",
		".",
		"..",
		"./a",
		"a/./b",
		"a/../b",
		"../a",
		`a\b`,
		"a\x00b",
		"a\nb",
		"a\xffb",
		strings.Repeat("a", MaxPathBytes+1),
	}
	for _, path := range invalidPaths {
		if err := ValidPath(path); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidPath(%q) = %v, want ErrInvalid", path, err)
		}
	}
}

func TestValidKind(t *testing.T) {
	valid := []Kind{KindTestResult, KindScreenshot, KindLog, KindDocument, "benchmark", "a1-b2"}
	for _, kind := range valid {
		if err := ValidKind(kind); err != nil {
			t.Errorf("ValidKind(%q) = %v, want nil", kind, err)
		}
	}

	invalidKinds := []Kind{"", "Log", "1log", "-log", "test_result", "test result", Kind(strings.Repeat("a", MaxKindBytes+1))}
	for _, kind := range invalidKinds {
		if err := ValidKind(kind); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidKind(%q) = %v, want ErrInvalid", kind, err)
		}
	}
}
