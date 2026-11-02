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

var defaultLimits = limits{minCost: minCost, work: maxWork}

// Hunks returns the unified diff hunks from a to b without file headers,
// like the patch fields of forge APIs, and the number of added and deleted
// lines. Each hunk has up to context unchanged lines around its changes; a
// negative context counts as 0. Hunks whose changes are at most 2*context
// lines apart are merged. Headers use git's form "@@ -l,s +l,s @@", where
// ",s" is left out when s is 1. Identical inputs give "".
func Hunks(a, b []byte, context int) (patch string, additions, deletions int) {
	return hunks(a, b, context, defaultLimits)
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

// split finds a point (x, y) on a shortest edit path through the box
// a[aLo:aHi], b[bLo:bHi], whose first and last lines differ. It searches
// forward and backward at once, as in Myers' linear-space algorithm and
// git's xdiff. Diagonal k holds the points with x-y == k. After maxCost
// rounds it returns the furthest point reached. It reports false when the
// work budget runs out or the point would not shrink the box.
func (d *differ) split(aLo, aHi, bLo, bHi int) (x, y int, ok bool) {
	off := len(d.b) + 1
	vf, vb := d.vf, d.vb
	dmin, dmax := aLo-bHi, aHi-bLo
	fmid, bmid := aLo-bLo, aHi-bHi
	fmin, fmax, bmin, bmax := fmid, fmid, bmid, bmid
	odd := (fmid-bmid)&1 != 0
	vf[off+fmid] = aLo
	vb[off+bmid] = aHi
	inside := func(x, y int) bool {
		return x >= aLo && x <= aHi && y >= bLo && y <= bHi &&
			x+y > aLo+bLo && x+y < aHi+bHi
	}
	for ec := 1; ; ec++ {
		if d.work <= 0 {
			return 0, 0, false
		}
		if fmin > dmin {
			fmin--
			vf[off+fmin-1] = -1
		} else {
			fmin++
		}
		if fmax < dmax {
			fmax++
			vf[off+fmax+1] = -1
		} else {
			fmax--
		}
		for k := fmax; k >= fmin; k -= 2 {
			if vf[off+k-1] >= vf[off+k+1] {
				x = vf[off+k-1] + 1
			} else {
				x = vf[off+k+1]
			}
			x0 := x
			y = x - k
			for x < aHi && y < bHi && d.a[x] == d.b[y] {
				x++
				y++
			}
			d.work -= 1 + x - x0
			vf[off+k] = x
			if odd && bmin <= k && k <= bmax && vb[off+k] <= x {
				return x, y, inside(x, y)
			}
		}

		if bmin > dmin {
			bmin--
			vb[off+bmin-1] = math.MaxInt
		} else {
			bmin++
		}
		if bmax < dmax {
			bmax++
			vb[off+bmax+1] = math.MaxInt
		} else {
			bmax--
		}
		for k := bmax; k >= bmin; k -= 2 {
			if vb[off+k-1] < vb[off+k+1] {
				x = vb[off+k-1]
			} else {
				x = vb[off+k+1] - 1
			}
			x0 := x
			y = x - k
			for x > aLo && y > bLo && d.a[x-1] == d.b[y-1] {
				x--
				y--
			}
			d.work -= 1 + x0 - x
			vb[off+k] = x
			if !odd && fmin <= k && k <= fmax && x <= vf[off+k] {
				return x, y, inside(x, y)
			}
		}

		if ec >= d.maxCost {
			fbest, fx := -1, 0
			for k := fmax; k >= fmin; k -= 2 {
				x := min(vf[off+k], aHi)
				y := x - k
				if y > bHi {
					x, y = bHi+k, bHi
				}
				if x+y > fbest {
					fbest, fx = x+y, x
				}
			}
			bbest, bx := math.MaxInt, 0
			for k := bmax; k >= bmin; k -= 2 {
				x := max(aLo, vb[off+k])
				y := x - k
				if y < bLo {
					x, y = bLo+k, bLo
				}
				if x+y < bbest {
					bbest, bx = x+y, x
				}
			}
			if (aHi+bHi)-bbest < fbest-(aLo+bLo) {
				x, y = fx, fbest-fx
			} else {
				x, y = bx, bbest-bx
			}
			return x, y, inside(x, y)
		}
	}
}

// op is one line of the edit script: ' ' kept, '-' deleted, '+' inserted.
type op struct {
	kind byte
	line []byte
}

// encode writes the marked lines as unified hunks.
func encode(la, lb [][]byte, delA, insB []bool, context int) (string, int, int) {
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
		end := c // just past the last change
		for e := c; e < n; {
			if ops[e].kind != ' ' {
				e++
				end = e
				continue
			}
			r := e
			for r < n && ops[r].kind == ' ' {
				r++
			}
			if r == n || r-e > 2*context {
				break
			}
			e = r
		}
		stop := min(n, end+context)

		oldN, newN := 0, 0
		for _, o := range ops[start:stop] {
			if o.kind != '+' {
				oldN++
			}
			if o.kind != '-' {
				newN++
			}
		}
		sb.WriteString("@@ -")
		writeRange(&sb, aPos, oldN)
		sb.WriteString(" +")
		writeRange(&sb, bPos, newN)
		sb.WriteString(" @@\n")
		for _, o := range ops[start:stop] {
			sb.WriteByte(o.kind)
			sb.Write(o.line)
			if len(o.line) == 0 || o.line[len(o.line)-1] != '\n' {
				sb.WriteString("\n\\ No newline at end of file\n")
			}
		}
		aPos += oldN
		bPos += newN
		i = stop
	}
	return sb.String(), adds, dels
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
