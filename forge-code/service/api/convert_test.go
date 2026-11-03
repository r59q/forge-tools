package api

import (
	"math"
	"testing"
)

func TestCount32Clamps(t *testing.T) {
	cases := map[int]int32{
		-1:                0,
		0:                 0,
		7:                 7,
		math.MaxInt32:     math.MaxInt32,
		math.MaxInt32 + 1: math.MaxInt32,
	}

	for in, want := range cases {
		if got := count32(in); got != want {
			t.Errorf("count32(%d) = %d, want %d", in, got, want)
		}
	}
}
