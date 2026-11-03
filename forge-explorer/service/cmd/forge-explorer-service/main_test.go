package main

import (
	"io"
	"testing"
)

func TestConfiguration(t *testing.T) {
	getenv := func(string) string { return "" }
	if _, err := parse(nil, getenv, io.Discard); err == nil {
		t.Fatal("unconfigured root accepted")
	}
	s, err := parse([]string{"--roots", "local=" + t.TempDir()}, getenv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.HTTPAddress != "127.0.0.1:8090" || s.GRPCAddress != "127.0.0.1:9100" {
		t.Fatal(s.Settings)
	}
	roots, err := openRoots(s.roots)
	if err != nil {
		t.Fatal(err)
	}
	closeRoots(roots)
	for _, spec := range []string{"relative", "x=relative", "../x=/tmp", "x=/does/not/exist"} {
		if _, err := openRoots(spec); err == nil {
			t.Fatal(spec)
		}
	}
}
