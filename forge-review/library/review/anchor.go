package review

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

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
