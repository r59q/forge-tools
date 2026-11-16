package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

func TestReadDocuments(t *testing.T) {
	_, root := checkout(t, map[string]string{
		"main.go":          "package main\n",
		"a-b.txt":          "dash\n",
		"a/b.txt":          "slash\n",
		"vendor/lib/x.go":  "package lib\n",
		".git/config":      "secret\n",
		".git/HEAD":        "ref\n",
		"module/.git":      "gitdir: ../.git/modules/module\n",
		"module/readme.md": "module\n",
		"empty.txt":        "",
	})

	tree := read(t, root, Options{Source: "checkout-1"})

	// Byte order puts "-" before "/", unlike a walk of sorted directories.
	want := []string{"a-b.txt", "a/b.txt", "empty.txt", "main.go", "module/readme.md", "vendor/lib/x.go"}
	if got := ids(tree.Documents); !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}

	main := tree.Documents[3]
	sum := sha256.Sum256([]byte("package main\n"))
	wantRef := search.Ref{Kind: search.KindCode, Source: "checkout-1", ID: "main.go", Version: hex.EncodeToString(sum[:])}
	if main.Ref != wantRef || string(main.Content) != "package main\n" {
		t.Errorf("document = %+v %q, want %+v", main.Ref, main.Content, wantRef)
	}
	if tree.State != (State{}) {
		t.Errorf("state = %+v, want nothing left out", tree.State)
	}
}

func TestReadSkipsSymlinks(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, map[string]string{"secret.txt": "outside secret\n", "dir/inner.txt": "outside inner\n"})

	dir, root := checkout(t, map[string]string{"main.go": "inside\n", "sub/deep/file.txt": "deep\n"})
	symlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(dir, "absolute"))
	symlink(t, filepath.Join(outside, "dir"), filepath.Join(dir, "directory"))
	symlink(t, "../../../"+filepath.Base(outside)+"/secret.txt", filepath.Join(dir, "sub/deep/relative"))
	symlink(t, "..", filepath.Join(dir, "sub/deep/parent"))
	symlink(t, "../main.go", filepath.Join(dir, "sub/inside"))

	tree := read(t, root, Options{})

	if got, want := ids(tree.Documents), []string{"main.go", "sub/deep/file.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %q, want %q", got, want)
	}
	if strings.Contains(contents(tree), "outside") {
		t.Error("read content from outside the root")
	}
	if tree.State.Unsupported != 5 {
		t.Errorf("unsupported = %d, want 5 symlinks", tree.State.Unsupported)
	}
}

func TestReadStaysInRootWhenDirectoryBecomesSymlink(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, map[string]string{"file.txt": "outside\n"})

	dir, root := checkout(t, map[string]string{"sub/file.txt": "inside\n"})
	listed, err := list(context.Background(), root, DefaultLimits(), "")
	if err != nil {
		t.Fatal(err)
	}

	// The directory is swapped for a symlink out of the root after listing.
	remove(t, dir, "sub")
	symlink(t, outside, filepath.Join(dir, "sub"))

	content, err := readFile(context.Background(), root, listed.files[0])
	if err == nil {
		t.Fatalf("read %q through a symlink out of the root", content)
	}
	if !errors.Is(err, ErrUnreadable) {
		t.Errorf("error = %v, want ErrUnreadable", err)
	}
	noHostPath(t, err, dir, "outside")
}

func TestReadSkipsInvalidNames(t *testing.T) {
	_, root := checkout(t, map[string]string{"bad\xff.txt": "x\n", "bad\xffdir/file.txt": "x\n", "good.txt": "x\n"})

	tree := read(t, root, Options{})

	if got := ids(tree.Documents); !reflect.DeepEqual(got, []string{"good.txt"}) {
		t.Errorf("paths = %q", got)
	}
	if tree.State.Unsupported != 2 {
		t.Errorf("unsupported = %d, want 2", tree.State.Unsupported)
	}
}

func TestReadKeepsBinaryFiles(t *testing.T) {
	_, root := checkout(t, map[string]string{"image.png": "\x89PNG\x00", "latin1.txt": "caf\xe9"})

	tree := read(t, root, Options{})

	if got := ids(tree.Documents); !reflect.DeepEqual(got, []string{"image.png", "latin1.txt"}) {
		t.Errorf("paths = %q, want binary files as documents", got)
	}
}

func TestReadInvalidSource(t *testing.T) {
	_, root := checkout(t, map[string]string{"a.txt": "a"})

	_, err := Read(context.Background(), root, Options{Source: "bad\x00"})
	if !errors.Is(err, search.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

func TestReadChangedFile(t *testing.T) {
	dir, root := checkout(t, map[string]string{"grows.txt": "small\n", "gone.txt": "here\n"})
	listed, err := list(context.Background(), root, DefaultLimits(), "")
	if err != nil {
		t.Fatal(err)
	}

	write(t, dir, map[string]string{"grows.txt": "larger content\n"})
	remove(t, dir, "gone.txt")

	for _, file := range listed.files {
		_, err := readFile(context.Background(), root, file)
		if !errors.Is(err, ErrChanged) {
			t.Errorf("%s: error = %v, want ErrChanged", file.path, err)
			continue
		}
		if !strings.Contains(err.Error(), file.path) {
			t.Errorf("error %q does not name %s", err, file.path)
		}
		noHostPath(t, err, dir, "small", "larger", "here")
	}
}

func TestReadUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files without permission")
	}
	dir, root := checkout(t, map[string]string{"locked.txt": "private\n"})
	if err := os.Chmod(filepath.Join(dir, "locked.txt"), 0); err != nil {
		t.Fatal(err)
	}

	_, err := Read(context.Background(), root, Options{})
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("error = %v, want ErrUnreadable", err)
	}
	noHostPath(t, err, dir, "private")
}

func TestReadCanceled(t *testing.T) {
	_, root := checkout(t, map[string]string{"a.txt": "a"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Read(ctx, root, Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}
