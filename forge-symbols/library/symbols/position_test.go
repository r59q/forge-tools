package symbols

import "testing"

func TestPositions(t *testing.T) {
	// "a", "é" (2 bytes), "\r\n", "世" (3 bytes), "😀" (4 bytes), an invalid
	// byte, "\n", "z".
	source := []byte("aé\r\n世😀\xff\nz")

	tests := []struct {
		offset int
		want   point
	}{
		{0, point{1, 1}},
		{1, point{1, 2}},
		{3, point{1, 3}},
		{4, point{1, 4}},  // "\r" counts as a column
		{5, point{2, 1}},  // after "\n"
		{8, point{2, 2}},  // after "世"
		{12, point{2, 4}}, // after "😀", two UTF-16 units
		{13, point{2, 5}}, // after the invalid byte
		{14, point{3, 1}},
		{15, point{3, 2}}, // end of source
	}

	var offsets []int
	for index := len(tests) - 1; index >= 0; index-- {
		offsets = append(offsets, tests[index].offset, tests[index].offset)
	}
	located := newPositions(source, offsets)

	for _, test := range tests {
		if got := located.at(test.offset); got != test.want {
			t.Errorf("offset %d = %+v, want %+v", test.offset, got, test.want)
		}
	}

	if got, want := located.span(1, 12), (Range{1, 2, 2, 4}); got != want {
		t.Errorf("span = %+v, want %+v", got, want)
	}
}

func TestPositionsOfEmptySource(t *testing.T) {
	located := newPositions(nil, []int{0})

	if got, want := located.span(0, 0), (Range{1, 1, 1, 1}); got != want {
		t.Errorf("span = %+v, want %+v", got, want)
	}
}
