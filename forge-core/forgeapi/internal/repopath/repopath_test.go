package repopath

import "testing"

func TestClean(t *testing.T) {
	good := []struct{ in, want string }{
		{"mrbean/teddy", "mrbean/teddy"},
		{" /group/sub/project/", "group/sub/project"},
		{"a.b/c-d_e", "a.b/c-d_e"},
	}
	for _, tt := range good {
		if got, ok := Clean(tt.in); !ok || got != tt.want {
			t.Errorf("Clean(%q) = %q, %v, want %q", tt.in, got, ok, tt.want)
		}
	}

	for _, p := range []string{"", "solo", "/solo/", "a//b", "a/../b", "a/./b", "a/b%2Fc", "a/b?x", "a/b#x", `a\b/c`, "a/b c", "a/b\tc", "a/b\x7f"} {
		if got, ok := Clean(p); ok {
			t.Errorf("Clean(%q) = %q, true, want false", p, got)
		}
	}
}
