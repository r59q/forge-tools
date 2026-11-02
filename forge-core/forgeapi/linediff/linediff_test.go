package linediff

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestHunks(t *testing.T) {
	tests := []struct {
		name     string
		a, b     string
		context  int
		want     string
		add, del int
	}{
		{name: "identical", a: "x\ny\n", b: "x\ny\n", context: 3},
		{name: "both empty", context: 3},
		{
			name: "from empty", b: "x\ny\n", context: 3,
			want: "@@ -0,0 +1,2 @@\n+x\n+y\n", add: 2,
		},
		{
			name: "to empty", a: "x\n", context: 3,
			want: "@@ -1 +0,0 @@\n-x\n", del: 1,
		},
		{
			name: "modify middle", a: "1\n2\n3\n4\n5\n6\n7\n8\n9\n", b: "1\n2\n3\n4\nfive\n6\n7\n8\n9\n", context: 3,
			want: "@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+five\n 6\n 7\n 8\n", add: 1, del: 1,
		},
		{
			name: "context 0", a: "1\n2\n3\n", b: "1\nb\n3\n", context: 0,
			want: "@@ -2 +2 @@\n-2\n+b\n", add: 1, del: 1,
		},
		{
			name: "pure insert context 0", a: "1\n2\n", b: "1\nx\n2\n", context: 0,
			want: "@@ -1,0 +2 @@\n+x\n", add: 1,
		},
		{
			name: "negative context", a: "1\n2\n3\n", b: "1\nb\n3\n", context: -5,
			want: "@@ -2 +2 @@\n-2\n+b\n", add: 1, del: 1,
		},
		{
			name: "merged hunks", a: "a\n1\n2\nb\n", b: "A\n1\n2\nB\n", context: 1,
			want: "@@ -1,4 +1,4 @@\n-a\n+A\n 1\n 2\n-b\n+B\n", add: 2, del: 2,
		},
		{
			name: "split hunks", a: "a\n1\n2\n3\nb\n", b: "A\n1\n2\n3\nB\n", context: 1,
			want: "@@ -1,2 +1,2 @@\n-a\n+A\n 1\n@@ -4,2 +4,2 @@\n 3\n-b\n+B\n", add: 2, del: 2,
		},
		{
			name: "add final newline", a: "x\ny", b: "x\ny\n", context: 3,
			want: "@@ -1,2 +1,2 @@\n x\n-y\n\\ No newline at end of file\n+y\n", add: 1, del: 1,
		},
		{
			name: "drop final newline", a: "x\ny\n", b: "x\ny", context: 3,
			want: "@@ -1,2 +1,2 @@\n x\n-y\n+y\n\\ No newline at end of file\n", add: 1, del: 1,
		},
		{
			name: "context without final newline", a: "a\nz", b: "b\nz", context: 3,
			want: "@@ -1,2 +1,2 @@\n-a\n+b\n z\n\\ No newline at end of file\n", add: 1, del: 1,
		},
		{
			name: "crlf kept", a: "a\r\nb\r\n", b: "a\r\nc\r\n", context: 3,
			want: "@@ -1,2 +1,2 @@\n a\r\n-b\r\n+c\r\n", add: 1, del: 1,
		},
		{
			name: "line ending change", a: "a\n", b: "a\r\n", context: 3,
			want: "@@ -1 +1 @@\n-a\n+a\r\n", add: 1, del: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, add, del := Hunks([]byte(tt.a), []byte(tt.b), tt.context)
			if got != tt.want || add != tt.add || del != tt.del {
				t.Fatalf("Hunks = %q, +%d -%d\nwant %q, +%d -%d", got, add, del, tt.want, tt.add, tt.del)
			}
			if b, err := apply([]byte(tt.a), got); err != nil || string(b) != tt.b {
				t.Fatalf("apply = %q, %v, want %q", b, err, tt.b)
			}
		})
	}
}

// TestProperty checks on random inputs that the patch turns a into b, that
// the counts match the patch, and that the diff is minimal.
func TestProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 3000 {
		a, b := randomPair(r)
		ctx := r.IntN(5)
		patch, add, del := Hunks(a, b, ctx)
		check(t, fmt.Sprintf("case %d", i), a, b, patch, add, del)
		lcs := lcsLen(splitLines(a), splitLines(b))
		if want := len(splitLines(b)) - lcs; add != want {
			t.Fatalf("case %d: additions = %d, want minimal %d\na=%q\nb=%q", i, add, want, a, b)
		}
		if want := len(splitLines(a)) - lcs; del != want {
			t.Fatalf("case %d: deletions = %d, want minimal %d", i, del, want)
		}
	}
}

// TestPropertyLimits forces the heuristic split and the work budget
// fallback; patches must stay valid.
func TestPropertyLimits(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for _, lim := range []limits{{1, maxWork}, {2, maxWork}, {1, 0}, {1, 7}, {3, 40}} {
		for i := range 1000 {
			a, b := randomPair(r)
			patch, add, del := hunks(a, b, r.IntN(4), lim)
			check(t, fmt.Sprintf("limits %+v case %d", lim, i), a, b, patch, add, del)
		}
	}
}

// TestPathological diffs large, unrelated inputs, where an unbounded
// search would be quadratic.
func TestPathological(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	var a, b bytes.Buffer
	for range 100000 {
		fmt.Fprintf(&a, "%d\n", r.IntN(4))
		fmt.Fprintf(&b, "%d\n", r.IntN(4))
	}
	patch, add, del := Hunks(a.Bytes(), b.Bytes(), 3)
	check(t, "pathological", a.Bytes(), b.Bytes(), patch, add, del)
}

func TestGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	r := rand.New(rand.NewPCG(7, 8))
	for i := range 150 {
		a, b := randomPair(r)
		if bytes.Equal(a, b) {
			continue
		}
		ctx := r.IntN(4)
		dir := t.TempDir()
		h, add, del := Hunks(a, b, ctx)
		patch := "--- a/f\n+++ b/f\n" + h
		writeFile(t, filepath.Join(dir, "f"), a)
		writeFile(t, filepath.Join(dir, "p"), []byte(patch))
		args := []string{"apply", "--whitespace=nowarn"}
		if ctx == 0 {
			args = append(args, "--unidiff-zero")
		}
		runGit(t, git, dir, append(args, "--check", "p")...)
		runGit(t, git, dir, append(args, "p")...)
		got, err := os.ReadFile(filepath.Join(dir, "f"))
		if errors.Is(err, os.ErrNotExist) {
			got, err = nil, nil
		}
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("case %d: git apply gave %q, %v, want %q\npatch:\n%s", i, got, err, b, patch)
		}

		// A minimal diff has the same counts as git's --minimal diff.
		writeFile(t, filepath.Join(dir, "a"), a)
		writeFile(t, filepath.Join(dir, "b"), b)
		out := runGit(t, git, dir, "diff", "--no-index", "--minimal", "--numstat", "a", "b")
		var gAdd, gDel int
		if _, err := fmt.Sscanf(out, "%d\t%d\t", &gAdd, &gDel); err != nil {
			t.Fatalf("case %d: numstat %q: %v", i, out, err)
		}
		if gAdd != add || gDel != del {
			t.Fatalf("case %d: +%d -%d, git says +%d -%d", i, add, del, gAdd, gDel)
		}
	}
}

// check verifies that patch turns a into b and that the counts match it.
func check(t *testing.T, name string, a, b []byte, patch string, add, del int) {
	t.Helper()
	if bytes.Equal(a, b) != (patch == "") {
		t.Fatalf("%s: patch %q for equal=%v", name, patch, bytes.Equal(a, b))
	}
	got, err := apply(a, patch)
	if err != nil || !bytes.Equal(got, b) {
		t.Fatalf("%s: apply = %q, %v, want %q\na=%q\npatch:\n%s", name, got, err, b, a, patch)
	}
	var pa, pd int
	for _, l := range strings.SplitAfter(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "+"):
			pa++
		case strings.HasPrefix(l, "-"):
			pd++
		}
	}
	if pa != add || pd != del {
		t.Fatalf("%s: counts +%d -%d, patch has +%d -%d", name, add, del, pa, pd)
	}
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@\n$`)

// apply applies unified hunks to a strictly: line numbers and context must
// match exactly.
func apply(a []byte, patch string) ([]byte, error) {
	src := splitLines(a)
	var out [][]byte
	pos := 0
	lines := strings.SplitAfter(patch, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) >= 2 && strings.HasPrefix(lines[0], "--- ") && strings.HasPrefix(lines[1], "+++ ") {
		lines = lines[2:]
	}
	for len(lines) > 0 {
		m := hunkHeader.FindStringSubmatch(lines[0])
		if m == nil {
			return nil, fmt.Errorf("bad hunk header %q", lines[0])
		}
		num := func(s string) int {
			if s == "" {
				return 1
			}
			n, _ := strconv.Atoi(s)
			return n
		}
		oldL, oldN, newL, newN := num(m[1]), num(m[2]), num(m[3]), num(m[4])
		lines = lines[1:]
		type entry struct {
			kind byte
			text []byte
		}
		var body []entry
		for len(lines) > 0 && !strings.HasPrefix(lines[0], "@@") {
			l := lines[0]
			lines = lines[1:]
			if l == "\\ No newline at end of file\n" {
				if len(body) == 0 {
					return nil, errors.New("marker without line")
				}
				t := body[len(body)-1].text
				body[len(body)-1].text = t[:len(t)-1]
				continue
			}
			if len(l) < 2 || !strings.ContainsRune(" -+", rune(l[0])) {
				return nil, fmt.Errorf("bad body line %q", l)
			}
			body = append(body, entry{l[0], []byte(l[1:])})
		}
		start := oldL - 1
		if oldN == 0 {
			start = oldL
		}
		wantNew := newL - 1
		if newN == 0 {
			wantNew = newL
		}
		if start < pos || start > len(src) {
			return nil, fmt.Errorf("hunk at old line %d out of order", oldL)
		}
		out = append(out, src[pos:start]...)
		pos = start
		if len(out) != wantNew {
			return nil, fmt.Errorf("hunk new line %d, but output is at %d", newL, len(out))
		}
		var gotOld, gotNew int
		for _, e := range body {
			if e.kind != '+' {
				if pos >= len(src) || !bytes.Equal(src[pos], e.text) {
					return nil, fmt.Errorf("mismatch at old line %d", pos+1)
				}
				pos++
				gotOld++
			}
			if e.kind != '-' {
				out = append(out, e.text)
				gotNew++
			}
		}
		if gotOld != oldN || gotNew != newN {
			return nil, fmt.Errorf("hunk counts -%d +%d, header -%d +%d", gotOld, gotNew, oldN, newN)
		}
	}
	out = append(out, src[pos:]...)
	return bytes.Join(out, nil), nil
}

// randomPair returns a random input and either an unrelated input or an
// edited copy, from a small line pool so lines repeat.
func randomPair(r *rand.Rand) (a, b []byte) {
	pool := []string{"a\n", "b\n", "c\n", "a\r\n", "\n", "d e\n", "f\r\n"}
	gen := func(n int) []string {
		ls := make([]string, n)
		for i := range ls {
			ls[i] = pool[r.IntN(len(pool))]
		}
		return ls
	}
	join := func(ls []string) []byte {
		s := strings.Join(ls, "")
		if s != "" && r.IntN(4) == 0 {
			s = strings.TrimSuffix(s, "\n") // drop the final newline
		}
		return []byte(s)
	}
	la := gen(r.IntN(25))
	var lb []string
	if r.IntN(3) == 0 {
		lb = gen(r.IntN(25))
	} else {
		lb = append([]string(nil), la...)
		for range r.IntN(6) {
			i := r.IntN(len(lb) + 1)
			switch r.IntN(3) {
			case 0:
				lb = append(lb[:i], append(gen(1+r.IntN(3)), lb[i:]...)...)
			case 1:
				if i < len(lb) {
					lb = append(lb[:i], lb[i+1:]...)
				}
			default:
				if i < len(lb) {
					lb[i] = pool[r.IntN(len(pool))]
				}
			}
		}
	}
	return join(la), join(lb)
}

func lcsLen(a, b [][]byte) int {
	prev, cur := make([]int, len(b)+1), make([]int, len(b)+1)
	for i := range a {
		for j := range b {
			if bytes.Equal(a[i], b[j]) {
				cur[j+1] = prev[j] + 1
			} else {
				cur[j+1] = max(prev[j+1], cur[j])
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// runGit runs git without user or system config. Exit status 1 counts as
// success, since git diff uses it to report differences.
func runGit(t *testing.T, git, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(git, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1 && args[0] == "diff") {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return out.String()
}
