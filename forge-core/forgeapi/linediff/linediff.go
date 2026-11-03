// Package linediff computes line diffs with the Myers algorithm and encodes
// them as unified diff hunks in git's format.
//
// A line ends after each '\n' and keeps its terminator, so CRLF endings
// and a missing final newline survive byte for byte. Lines compare as raw
// bytes; there is no whitespace or encoding handling.
//
// Cost is bounded. Like git's xdiff, one search runs at most
// max(256, sqrt(lines)) edit rounds before it splits at the furthest point
// reached, and a whole diff takes at most maxWork search steps. Past the
// budget, the remaining changed ranges become plain delete-then-insert
// blocks. The patch then is still valid but no longer minimal. Memory and
// the remaining work grow linearly with len(a)+len(b); callers cap input
// sizes.
package linediff

import (
	"bytes"
	"math"
	"strconv"
	"strings"
)

const (
	// minCost is the least number of edit rounds one search runs before it
	// splits heuristically.
	minCost = 256
	// maxWork caps the search steps of one diff.
	maxWork = 1 << 25
)

// limits tunes the search; tests shrink them to reach the fallbacks.
type limits struct {
	minCost int
	work    int
}

// Hunks returns the unified diff hunks from a to b without file headers,
// like the patch fields of forge APIs, and the number of added and deleted
// lines. Each hunk has up to context unchanged lines around its changes; a
// negative context counts as 0. Hunks whose changes are at most 2*context
// lines apart are merged. Headers use git's form "@@ -l,s +l,s @@", where
// ",s" is left out when s is 1. Identical inputs give "".
func Hunks(a, b []byte, context int) (patch string, additions, deletions int) {
	return hunks(a, b, context, limits{minCost: minCost, work: maxWork})
}

func hunks(a, b []byte, context int, lim limits) (string, int, int) {
	if bytes.Equal(a, b) {
		return "", 0, 0
	}

	context = max(context, 0)
	la, lb := splitLines(a), splitLines(b)
	d := newDiffer(la, lb, lim)
	d.run()

	return encode(la, lb, d.delA, d.insB, context)
}

// splitLines splits s after each '\n'. The last line may lack one.
func splitLines(s []byte) [][]byte {
	ls := make([][]byte, 0, bytes.Count(s, []byte{'\n'})+1)
	for len(s) > 0 {
		i := bytes.IndexByte(s, '\n')
		if i < 0 {
			ls = append(ls, s)
			break
		}

		ls = append(ls, s[:i+1])
		s = s[i+1:]
	}

	return ls
}

// differ marks the lines of a to delete and of b to insert.
type differ struct {
	a, b       []int // line IDs
	delA, insB []bool
	vf, vb     []int // furthest x per diagonal, forward and backward
	maxCost    int
	work       int
}

func newDiffer(la, lb [][]byte, lim limits) *differ {
	ids := make(map[string]int)
	intern := func(ls [][]byte) []int {
		out := make([]int, len(ls))
		for i, l := range ls {
			id, ok := ids[string(l)]
			if !ok {
				id = len(ids)
				ids[string(l)] = id
			}

			out[i] = id
		}

		return out
	}
	n := len(la) + len(lb)

	return &differ{
		a:       intern(la),
		b:       intern(lb),
		delA:    make([]bool, len(la)),
		insB:    make([]bool, len(lb)),
		vf:      make([]int, n+3),
		vb:      make([]int, n+3),
		maxCost: max(lim.minCost, int(math.Sqrt(float64(n+3)))),
		work:    lim.work,
	}
}

// run diffs the whole input. It keeps a stack of ranges instead of
// recursing, so heuristic splits cannot grow the call stack.
func (d *differ) run() {
	stack := [][4]int{{0, len(d.a), 0, len(d.b)}}
	for len(stack) > 0 {
		r := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		aLo, aHi, bLo, bHi := r[0], r[1], r[2], r[3]
		for aLo < aHi && bLo < bHi && d.a[aLo] == d.b[bLo] {
			aLo++
			bLo++
		}

		for aLo < aHi && bLo < bHi && d.a[aHi-1] == d.b[bHi-1] {
			aHi--
			bHi--
		}

		if aLo == aHi || bLo == bHi {
			d.replace(aLo, aHi, bLo, bHi)
			continue
		}

		x, y, ok := d.split(aLo, aHi, bLo, bHi)
		if !ok {
			d.replace(aLo, aHi, bLo, bHi)
			continue
		}

		stack = append(stack, [4]int{x, aHi, y, bHi}, [4]int{aLo, x, bLo, y})
	}
}

// replace marks a[aLo:aHi] deleted and b[bLo:bHi] inserted.
func (d *differ) replace(aLo, aHi, bLo, bHi int) {
	for i := aLo; i < aHi; i++ {
		d.delA[i] = true
	}

	for j := bLo; j < bHi; j++ {
		d.insB[j] = true
	}
}

// op is one line of the edit script: ' ' kept, '-' deleted, '+' inserted.
type op struct {
	kind byte
	line []byte
}

// encode writes the marked lines as unified hunks.
func encode(la, lb [][]byte, delA, insB []bool, context int) (string, int, int) {
	ops, adds, dels := editScript(la, lb, delA, insB)

	var sb strings.Builder
	n := len(ops)
	aPos, bPos := 0, 0 // lines of a and b before ops[i]
	for i := 0; ; {
		c := i
		for c < n && ops[c].kind == ' ' {
			c++
		}

		if c == n {
			break
		}

		start := max(i, c-context)
		aPos += start - i
		bPos += start - i
		stop := hunkStop(ops, c, context)
		oldN, newN := writeHunk(&sb, ops[start:stop], aPos, bPos)

		aPos += oldN
		bPos += newN
		i = stop
	}

	return sb.String(), adds, dels
}

func editScript(la, lb [][]byte, delA, insB []bool) ([]op, int, int) {
	ops := make([]op, 0, max(len(la), len(lb)))
	adds, dels := 0, 0
	for i, j := 0, 0; i < len(la) || j < len(lb); {
		switch {
		case i < len(la) && delA[i]:
			ops = append(ops, op{'-', la[i]})
			i++
			dels++
		case j < len(lb) && insB[j]:
			ops = append(ops, op{'+', lb[j]})
			j++
			adds++
		default:
			ops = append(ops, op{' ', la[i]})
			i++
			j++
		}
	}

	return ops, adds, dels
}

func hunkStop(ops []op, change, context int) int {
	end := change
	for next := change; next < len(ops); {
		if ops[next].kind != ' ' {
			next++
			end = next
			continue
		}

		unchanged := next
		for unchanged < len(ops) && ops[unchanged].kind == ' ' {
			unchanged++
		}

		if unchanged == len(ops) || unchanged-next > 2*context {
			break
		}

		next = unchanged
	}

	return min(len(ops), end+context)
}

func writeHunk(sb *strings.Builder, ops []op, oldPos, newPos int) (int, int) {
	oldCount, newCount := 0, 0
	for _, operation := range ops {
		if operation.kind != '+' {
			oldCount++
		}
		if operation.kind != '-' {
			newCount++
		}
	}

	sb.WriteString("@@ -")
	writeRange(sb, oldPos, oldCount)
	sb.WriteString(" +")
	writeRange(sb, newPos, newCount)
	sb.WriteString(" @@\n")
	for _, operation := range ops {
		sb.WriteByte(operation.kind)
		sb.Write(operation.line)
		if len(operation.line) == 0 || operation.line[len(operation.line)-1] != '\n' {
			sb.WriteString("\n\\ No newline at end of file\n")
		}
	}

	return oldCount, newCount
}

// writeRange writes "l,s" for s lines after line before, as git does: l is
// 1-based, or the line before an empty range, and ",s" is dropped when s
// is 1.
func writeRange(sb *strings.Builder, before, s int) {
	l := before
	if s > 0 {
		l++
	}

	sb.WriteString(strconv.Itoa(l))
	if s != 1 {
		sb.WriteByte(',')
		sb.WriteString(strconv.Itoa(s))
	}
}
