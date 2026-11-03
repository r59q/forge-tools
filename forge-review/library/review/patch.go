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

type patchParser struct {
	data    patchData
	file    *File
	headers bool
	old     int
	new     int
	oldLeft int
	newLeft int
	oldEnd  int
	newEnd  int
}

func parsePatch(p string) (patchData, error) {
	parser := patchParser{data: patchData{
		lines: map[fileSide]map[int]string{},
		pairs: map[fileSide]map[int]int{},
	}}
	if len(p) > maxSnapshot {
		return parser.data, ErrTooLarge
	}
	if !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
		return parser.data, ErrInvalid
	}

	lines := strings.Split(p, "\n")
	if len(lines) > maxLines {
		return parser.data, ErrTooLarge
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if parser.oldLeft > 0 || parser.newLeft > 0 {
			if err := parser.hunkLine(line); err != nil {
				return parser.data, err
			}

			continue
		}

		var err error
		switch {
		case strings.HasPrefix(line, "diff --git "):
			err = parser.startFile(line)
		case strings.HasPrefix(line, "--- "):
			i, err = parser.fileHeaders(lines, i)
		case strings.HasPrefix(line, "@@"):
			err = parser.startHunk(line)
		default:
			err = parser.metadata(line, i == len(lines)-1)
		}

		if err != nil {
			return parser.data, err
		}
	}

	if err := parser.flush(); err != nil {
		return parser.data, err
	}
	if err := uniquePatchFiles(parser.data.files); err != nil {
		return parser.data, err
	}

	return parser.data, nil
}

func (p *patchParser) flush() error {
	if p.oldLeft != 0 || p.newLeft != 0 {
		return ErrInvalid
	}
	if p.file == nil {
		return nil
	}
	if p.file.OldPath == "" && p.file.Path == "" {
		return ErrInvalid
	}

	p.data.files = append(p.data.files, *p.file)

	return nil
}

func (p *patchParser) startFile(line string) error {
	if err := p.flush(); err != nil {
		return err
	}

	oldPath, newPath, err := gitPaths(strings.TrimPrefix(line, "diff --git "))
	if err != nil {
		return err
	}

	p.file = &File{OldPath: oldPath, Path: newPath}
	p.headers = false
	p.oldEnd, p.newEnd = 0, 0

	return nil
}

func (p *patchParser) fileHeaders(lines []string, i int) (int, error) {
	if p.headers {
		if err := p.flush(); err != nil {
			return i, err
		}

		p.file = nil
	}

	if p.file == nil {
		p.file = &File{}
		p.oldEnd, p.newEnd = 0, 0
	}

	oldPath, err := headerPath(lines[i][4:])
	if err != nil {
		return i, err
	}

	i++
	if i >= len(lines) || !strings.HasPrefix(lines[i], "+++ ") {
		return i, ErrInvalid
	}

	newPath, err := headerPath(lines[i][4:])
	if err != nil {
		return i, err
	}

	p.file.OldPath = oldPath
	p.file.Path = newPath
	p.headers = true

	return i, nil
}

func (p *patchParser) startHunk(line string) error {
	if p.file == nil || !p.headers || p.file.Binary {
		return ErrInvalid
	}

	match := hunkHeader.FindStringSubmatch(line)
	if match == nil {
		return ErrInvalid
	}

	values := []int{0, 1, 0, 1}
	for i := range values {
		if match[i+1] == "" {
			continue
		}

		n, err := strconv.Atoi(match[i+1])
		if err != nil || n < 0 || n > 1000000000 {
			return ErrInvalid
		}

		values[i] = n
	}

	p.old, p.oldLeft, p.new, p.newLeft = values[0], values[1], values[2], values[3]
	oldStart, newStart := p.old, p.new
	if p.oldLeft == 0 {
		oldStart++
	}
	if p.newLeft == 0 {
		newStart++
	}
	if oldStart < p.oldEnd || newStart < p.newEnd {
		return ErrInvalid
	}

	p.oldEnd, p.newEnd = oldStart+p.oldLeft, newStart+p.newLeft

	if p.oldLeft == 0 && p.newLeft == 0 {
		return ErrInvalid
	}
	if (p.oldLeft > 0 && p.old == 0) || (p.newLeft > 0 && p.new == 0) {
		return ErrInvalid
	}

	return nil
}

func (p *patchParser) addLine(path, side string, n int, value string) error {
	if path == "" || n <= 0 {
		return ErrInvalid
	}

	key := fileSide{path, side}
	if p.data.lines[key] == nil {
		p.data.lines[key] = map[int]string{}
	}
	if _, exists := p.data.lines[key][n]; exists {
		return ErrInvalid
	}

	p.data.lines[key][n] = value

	return nil
}

func (p *patchParser) pairLine(path, side string, line, other int) {
	key := fileSide{path, side}
	if p.data.pairs[key] == nil {
		p.data.pairs[key] = map[int]int{}
	}

	p.data.pairs[key][line] = other
}

func (p *patchParser) hunkLine(line string) error {
	if line == `\ No newline at end of file` {
		return nil
	}
	if line == "" {
		return ErrInvalid
	}

	switch line[0] {
	case ' ':
		if p.oldLeft == 0 || p.newLeft == 0 {
			return ErrInvalid
		}
		if err := p.addLine(p.file.OldPath, "old", p.old, line[1:]); err != nil {
			return err
		}
		if err := p.addLine(p.file.Path, "new", p.new, line[1:]); err != nil {
			return err
		}
		p.pairLine(p.file.OldPath, "old", p.old, p.new)
		p.pairLine(p.file.Path, "new", p.new, p.old)
		p.old++
		p.new++
		p.oldLeft--
		p.newLeft--
	case '-':
		if p.oldLeft == 0 {
			return ErrInvalid
		}
		if err := p.addLine(p.file.OldPath, "old", p.old, line[1:]); err != nil {
			return err
		}
		p.old++
		p.oldLeft--
	case '+':
		if p.newLeft == 0 {
			return ErrInvalid
		}
		if err := p.addLine(p.file.Path, "new", p.new, line[1:]); err != nil {
			return err
		}
		p.new++
		p.newLeft--
	default:
		return ErrInvalid
	}

	return nil
}

func (p *patchParser) metadata(line string, last bool) error {
	switch {
	case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
		if p.file == nil {
			return ErrInvalid
		}
		p.file.Binary = true
	case line == "GIT binary patch":
		return fmt.Errorf("%w: encoded binary patch", ErrUnsupported)
	case strings.HasPrefix(line, "new file mode "):
		if p.file == nil {
			return ErrInvalid
		}
		p.file.OldPath = ""
	case strings.HasPrefix(line, "deleted file mode "):
		if p.file == nil {
			return ErrInvalid
		}
		p.file.Path = ""
	case line == "" && last, line == `\ No newline at end of file`:
	default:
		if !patchMetadata(line) || p.file == nil {
			return ErrInvalid
		}
	}

	return nil
}

func patchMetadata(line string) bool {
	prefixes := []string{
		"index ", "old mode ", "new mode ", "similarity index ", "dissimilarity index ",
		"rename from ", "rename to ", "copy from ", "copy to ",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}

	return false
}

func uniquePatchFiles(files []File) error {
	seen := map[fileSide]bool{}
	for _, file := range files {
		for _, key := range []fileSide{{file.OldPath, "old"}, {file.Path, "new"}} {
			if key.path == "" {
				continue
			}
			if seen[key] {
				return ErrInvalid
			}

			seen[key] = true
		}
	}

	return nil
}
