package review

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxSnapshot = 16 << 20
const maxLines = 200000

var hunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@(?:.*)$`)

type fileSide struct{ path, side string }
type patchData struct {
	files []File
	lines map[fileSide]map[int]string
	pairs map[fileSide]map[int]int
}

func filePath(v string) bool {
	if v == "" || strings.HasPrefix(v, "/") || strings.ContainsAny(v, "\x00\r\n\\") {
		return false
	}
	for _, p := range strings.Split(v, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return utf8.ValidString(v)
}
func headerPath(v string) (string, error) {
	v = strings.SplitN(v, "\t", 2)[0]
	if strings.HasPrefix(v, `"`) {
		s, e := strconv.Unquote(v)
		if e != nil {
			return "", ErrInvalid
		}
		v = s
	}
	if v == "/dev/null" {
		return "", nil
	}
	if strings.HasPrefix(v, "a/") || strings.HasPrefix(v, "b/") {
		v = v[2:]
	}
	if !filePath(v) {
		return "", ErrInvalid
	}
	return v, nil
}
func gitPaths(v string) (string, string, error) {
	// Git quotes paths containing controls or non-ASCII. Ordinary spaces remain
	// unquoted, so split on the b/ prefix rather than on arbitrary whitespace.
	var left, right string
	if strings.HasPrefix(v, `"`) {
		end := 1
		for end < len(v) {
			if v[end] == '\\' {
				end += 2
				continue
			}
			if v[end] == '"' {
				break
			}
			end++
		}
		if end >= len(v) {
			return "", "", ErrInvalid
		}
		left = v[:end+1]
		right = strings.TrimPrefix(v[end+1:], " ")
	} else {
		n := strings.Index(v, " b/")
		if n < 0 {
			n = strings.Index(v, ` "b/`)
		}
		if n < 0 {
			return "", "", ErrInvalid
		}
		left, right = v[:n], v[n+1:]
	}
	a, e := headerPath(left)
	if e != nil {
		return "", "", e
	}
	b, e := headerPath(right)
	return a, b, e
}
func parsePatch(p string) (patchData, error) {
	d := patchData{lines: map[fileSide]map[int]string{}, pairs: map[fileSide]map[int]int{}}
	if len(p) > maxSnapshot {
		return d, ErrTooLarge
	}
	if !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
		return d, ErrInvalid
	}
	lines := strings.Split(p, "\n")
	if len(lines) > maxLines {
		return d, ErrTooLarge
	}
	var f *File
	var old, new, oldLeft, newLeft int
	var headers bool
	var oldEnd, newEnd int
	flush := func() error {
		if oldLeft != 0 || newLeft != 0 {
			return ErrInvalid
		}
		if f != nil {
			if f.OldPath == "" && f.Path == "" {
				return ErrInvalid
			}
			d.files = append(d.files, *f)
		}
		return nil
	}
	add := func(path, side string, n int, value string) error {
		if path == "" || n <= 0 {
			return ErrInvalid
		}
		k := fileSide{path, side}
		if d.lines[k] == nil {
			d.lines[k] = map[int]string{}
		}
		if _, ok := d.lines[k][n]; ok {
			return ErrInvalid
		}
		d.lines[k][n] = value
		return nil
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if oldLeft > 0 || newLeft > 0 {
			if line == `\ No newline at end of file` {
				continue
			}
			if line == "" {
				return d, ErrInvalid
			}
			switch line[0] {
			case ' ':
				if oldLeft == 0 || newLeft == 0 {
					return d, ErrInvalid
				}
				if err := add(f.OldPath, "old", old, line[1:]); err != nil {
					return d, err
				}
				if err := add(f.Path, "new", new, line[1:]); err != nil {
					return d, err
				}
				for k, n := range map[fileSide]int{{f.OldPath, "old"}: old, {f.Path, "new"}: new} {
					if d.pairs[k] == nil {
						d.pairs[k] = map[int]int{}
					}
					other := new
					if k.side == "new" {
						other = old
					}
					d.pairs[k][n] = other
				}
				old++
				new++
				oldLeft--
				newLeft--
			case '-':
				if oldLeft == 0 {
					return d, ErrInvalid
				}
				if err := add(f.OldPath, "old", old, line[1:]); err != nil {
					return d, err
				}
				old++
				oldLeft--
			case '+':
				if newLeft == 0 {
					return d, ErrInvalid
				}
				if err := add(f.Path, "new", new, line[1:]); err != nil {
					return d, err
				}
				new++
				newLeft--
			default:
				return d, ErrInvalid
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			if err := flush(); err != nil {
				return d, err
			}
			a, b, e := gitPaths(strings.TrimPrefix(line, "diff --git "))
			if e != nil {
				return d, e
			}
			f = &File{OldPath: a, Path: b}
			headers = false
			oldEnd, newEnd = 0, 0
		case strings.HasPrefix(line, "--- "):
			if headers {
				if err := flush(); err != nil {
					return d, err
				}
				f = nil
			}
			if f == nil {
				f = &File{}
				oldEnd, newEnd = 0, 0
			}
			a, e := headerPath(line[4:])
			if e != nil {
				return d, e
			}
			i++
			if i >= len(lines) || !strings.HasPrefix(lines[i], "+++ ") {
				return d, ErrInvalid
			}
			b, e := headerPath(lines[i][4:])
			if e != nil {
				return d, e
			}
			f.OldPath = a
			f.Path = b
			headers = true
		case strings.HasPrefix(line, "@@"):
			if f == nil || !headers || f.Binary {
				return d, ErrInvalid
			}
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return d, ErrInvalid
			}
			vals := []int{0, 1, 0, 1}
			for j := 0; j < 4; j++ {
				if m[j+1] != "" {
					n, e := strconv.Atoi(m[j+1])
					if e != nil || n < 0 || n > 1000000000 {
						return d, ErrInvalid
					}
					vals[j] = n
				}
			}
			old, oldLeft, new, newLeft = vals[0], vals[1], vals[2], vals[3]
			oldStart, newStart := old, new
			if oldLeft == 0 {
				oldStart++
			}
			if newLeft == 0 {
				newStart++
			}
			if oldStart < oldEnd || newStart < newEnd {
				return d, ErrInvalid
			}
			oldEnd, newEnd = oldStart+oldLeft, newStart+newLeft
			if oldLeft == 0 && newLeft == 0 {
				return d, ErrInvalid
			}
			if (oldLeft > 0 && old == 0) || (newLeft > 0 && new == 0) {
				return d, ErrInvalid
			}
		case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
			if f == nil {
				return d, ErrInvalid
			}
			f.Binary = true
		case line == "GIT binary patch":
			return d, fmt.Errorf("%w: encoded binary patch", ErrUnsupported)
		case strings.HasPrefix(line, "new file mode "):
			if f == nil {
				return d, ErrInvalid
			}
			f.OldPath = ""
		case strings.HasPrefix(line, "deleted file mode "):
			if f == nil {
				return d, ErrInvalid
			}
			f.Path = ""
		case line == "" && i == len(lines)-1, line == `\ No newline at end of file`:
		case strings.HasPrefix(line, "index "), strings.HasPrefix(line, "old mode "), strings.HasPrefix(line, "new mode "), strings.HasPrefix(line, "similarity index "), strings.HasPrefix(line, "dissimilarity index "), strings.HasPrefix(line, "rename from "), strings.HasPrefix(line, "rename to "), strings.HasPrefix(line, "copy from "), strings.HasPrefix(line, "copy to "):
			if f == nil {
				return d, ErrInvalid
			}
		default:
			return d, ErrInvalid
		}
	}
	if err := flush(); err != nil {
		return d, err
	}
	seen := map[fileSide]bool{}
	for _, f := range d.files {
		for _, k := range []fileSide{{f.OldPath, "old"}, {f.Path, "new"}} {
			if k.path == "" {
				continue
			}
			if seen[k] {
				return d, ErrInvalid
			}
			seen[k] = true
		}
	}
	return d, nil
}
func textLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
func checkContext(s Subject, d patchData) error {
	size := len(s.Patch)
	seen := map[fileSide]bool{}
	for _, f := range s.Context {
		size += len(f.Content)
		if size > maxSnapshot {
			return ErrTooLarge
		}
		k := fileSide{f.Path, f.Side}
		if seen[k] || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) {
			return ErrInvalid
		}
		seen[k] = true
		if !hasFile(d.files, k, false) {
			return ErrInvalid
		}
		lines := textLines(f.Content)
		if len(lines) > maxLines {
			return ErrTooLarge
		}
		for n, v := range d.lines[k] {
			if n > len(lines) || lines[n-1] != v {
				return fmt.Errorf("%w: context disagrees with patch", ErrInvalid)
			}
		}
	}
	return nil
}
func hasFile(files []File, k fileSide, binary bool) bool {
	for _, f := range files {
		if !binary && f.Binary {
			continue
		}
		if (k.side == "old" && k.path == f.OldPath) || (k.side == "new" && k.path == f.Path) {
			return k.path != ""
		}
	}
	return false
}

// ValidateAnchor checks every line of an inclusive range against the snapshot.
// File comments require a known side; line comments need hunk or full-file text.
func ValidateAnchor(s Subject, a *Anchor) error {
	if a == nil {
		return nil
	}
	d, err := parsePatch(s.Patch)
	if err != nil {
		return err
	}
	k := fileSide{a.Path, a.Side}
	if !hasFile(d.files, k, true) || a.Start < 0 || a.End < a.Start {
		return ErrInvalid
	}
	if a.Start == 0 {
		if a.End != 0 {
			return ErrInvalid
		}
		return nil
	}
	if !hasFile(d.files, k, false) || a.End-a.Start >= maxLines {
		return ErrInvalid
	}
	for _, f := range s.Context {
		if f.Path == a.Path && f.Side == a.Side {
			if a.End <= len(textLines(f.Content)) {
				return nil
			}
			return ErrInvalid
		}
	}
	for n := a.Start; n <= a.End; n++ {
		if _, ok := d.lines[k][n]; !ok {
			return ErrInvalid
		}
	}
	return nil
}
