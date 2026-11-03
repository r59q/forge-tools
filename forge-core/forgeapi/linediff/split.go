package linediff

import "math"

// split finds a point (x, y) on a shortest edit path through the box
// a[aLo:aHi], b[bLo:bHi], whose first and last lines differ. It searches
// forward and backward at once, as in Myers' linear-space algorithm and
// git's xdiff. Diagonal k holds the points with x-y == k. After maxCost
// rounds it returns the furthest point reached. It reports false when the
// work budget runs out or the point would not shrink the box.
func (d *differ) split(aLo, aHi, bLo, bHi int) (int, int, bool) {
	s := d.newSearch(aLo, aHi, bLo, bHi)
	for ec := 1; ; ec++ {
		if d.work <= 0 {
			return 0, 0, false
		}

		if x, y, met := s.forward(); met {
			return x, y, s.inside(x, y)
		}

		if x, y, met := s.backward(); met {
			return x, y, s.inside(x, y)
		}

		if ec >= d.maxCost {
			x, y := s.furthest()
			return x, y, s.inside(x, y)
		}
	}
}

// search is the state of one split: the box, the diagonal offset into the
// differ's vectors, the bounding diagonals and the live diagonal ranges of
// both directions.
type search struct {
	d                      *differ
	off                    int
	aLo, aHi, bLo, bHi     int
	dmin, dmax             int
	fmin, fmax, bmin, bmax int
	odd                    bool
}

func (d *differ) newSearch(aLo, aHi, bLo, bHi int) *search {
	fmid, bmid := aLo-bLo, aHi-bHi
	s := &search{
		d:    d,
		off:  len(d.b) + 1,
		aLo:  aLo,
		aHi:  aHi,
		bLo:  bLo,
		bHi:  bHi,
		dmin: aLo - bHi,
		dmax: aHi - bLo,
		fmin: fmid,
		fmax: fmid,
		bmin: bmid,
		bmax: bmid,
		odd:  (fmid-bmid)&1 != 0,
	}

	d.vf[s.off+fmid] = aLo
	d.vb[s.off+bmid] = aHi

	return s
}

// inside reports whether (x, y) splits the box into two smaller boxes.
func (s *search) inside(x, y int) bool {
	return x >= s.aLo && x <= s.aHi && y >= s.bLo && y <= s.bHi &&
		x+y > s.aLo+s.bLo && x+y < s.aHi+s.bHi
}

// grow widens [lo, hi] by one diagonal on each side that stays within the
// box, or shrinks it on sides that cannot, and fences new diagonals with
// sentinel.
func (s *search) grow(v []int, lo, hi *int, sentinel int) {
	if *lo > s.dmin {
		*lo--
		v[s.off+*lo-1] = sentinel
	} else {
		*lo++
	}

	if *hi < s.dmax {
		*hi++
		v[s.off+*hi+1] = sentinel
	} else {
		*hi--
	}
}

// forward runs one forward round. It reports whether a path met the
// backward search, and where.
func (s *search) forward() (x, y int, met bool) {
	d, vf, off := s.d, s.d.vf, s.off
	s.grow(vf, &s.fmin, &s.fmax, -1)
	for k := s.fmax; k >= s.fmin; k -= 2 {
		if vf[off+k-1] >= vf[off+k+1] {
			x = vf[off+k-1] + 1
		} else {
			x = vf[off+k+1]
		}

		x0 := x
		y = x - k
		for x < s.aHi && y < s.bHi && d.a[x] == d.b[y] {
			x++
			y++
		}

		d.work -= 1 + x - x0
		vf[off+k] = x
		if s.odd && s.bmin <= k && k <= s.bmax && d.vb[off+k] <= x {
			return x, y, true
		}
	}

	return 0, 0, false
}

// backward runs one backward round. It reports whether a path met the
// forward search, and where.
func (s *search) backward() (x, y int, met bool) {
	d, vb, off := s.d, s.d.vb, s.off
	s.grow(vb, &s.bmin, &s.bmax, math.MaxInt)
	for k := s.bmax; k >= s.bmin; k -= 2 {
		if vb[off+k-1] < vb[off+k+1] {
			x = vb[off+k-1]
		} else {
			x = vb[off+k+1] - 1
		}

		x0 := x
		y = x - k
		for x > s.aLo && y > s.bLo && d.a[x-1] == d.b[y-1] {
			x--
			y--
		}

		d.work -= 1 + x0 - x
		vb[off+k] = x
		if !s.odd && s.fmin <= k && k <= s.fmax && x <= d.vf[off+k] {
			return x, y, true
		}
	}

	return 0, 0, false
}

// furthest returns the point of either search that got furthest from its
// start, for a heuristic split.
func (s *search) furthest() (x, y int) {
	fbest, fx := s.furthestForward()
	bbest, bx := s.furthestBackward()
	if (s.aHi+s.bHi)-bbest < fbest-(s.aLo+s.bLo) {
		return fx, fbest - fx
	}

	return bx, bbest - bx
}

// furthestForward returns the largest x+y the forward search reached, and
// its x.
func (s *search) furthestForward() (best, bestX int) {
	best = -1
	for k := s.fmax; k >= s.fmin; k -= 2 {
		x := min(s.d.vf[s.off+k], s.aHi)
		y := x - k
		if y > s.bHi {
			x, y = s.bHi+k, s.bHi
		}

		if x+y > best {
			best, bestX = x+y, x
		}
	}

	return best, bestX
}

// furthestBackward returns the smallest x+y the backward search reached,
// and its x.
func (s *search) furthestBackward() (best, bestX int) {
	best = math.MaxInt
	for k := s.bmax; k >= s.bmin; k -= 2 {
		x := max(s.aLo, s.d.vb[s.off+k])
		y := x - k
		if y < s.bLo {
			x, y = s.bLo+k, s.bLo
		}

		if x+y < best {
			best, bestX = x+y, x
		}
	}

	return best, bestX
}
