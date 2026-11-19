package evidence

import (
	"errors"
	"testing"
)

func TestLimitsWithDefaults(t *testing.T) {
	if got := (Limits{}).WithDefaults(); got != DefaultLimits() {
		t.Fatalf("zero limits = %+v, want defaults", got)
	}

	negative := Limits{MetadataBytes: -1, FileBytes: -1, Files: -1, EntryBytes: -1, Entries: -1, Problems: -1}
	if got := negative.WithDefaults(); got != DefaultLimits() {
		t.Fatalf("negative limits = %+v, want defaults", got)
	}

	custom := Limits{MetadataBytes: 1, FileBytes: 2, Files: 3, EntryBytes: 4, Entries: 5, Problems: 6}
	if got := custom.WithDefaults(); got != custom {
		t.Fatalf("custom limits = %+v, want %+v", got, custom)
	}

	partial := Limits{Files: 3}.WithDefaults()
	if partial.Files != 3 || partial.FileBytes != DefaultLimits().FileBytes {
		t.Fatalf("partial limits = %+v", partial)
	}
}

func TestDefaultLimits(t *testing.T) {
	want := Limits{
		MetadataBytes: 64 << 10,
		FileBytes:     4 << 20,
		Files:         32,
		EntryBytes:    16 << 20,
		Entries:       10000,
		Problems:      1000,
	}
	if got := DefaultLimits(); got != want {
		t.Fatalf("DefaultLimits = %+v, want the README table %+v", got, want)
	}
}

func TestValidateLimits(t *testing.T) {
	entry := exampleEntry()

	cases := []struct {
		limits Limits
		want   error
	}{
		{Limits{}, nil},
		{Limits{Files: 2, FileBytes: 2048, EntryBytes: 2048}, nil},
		{Limits{Files: 1}, ErrTooLarge},
		{Limits{FileBytes: 2047}, ErrTooLarge},
		{Limits{EntryBytes: 2047}, ErrTooLarge},
	}
	for _, test := range cases {
		if err := entry.Validate(test.limits); !errors.Is(err, test.want) {
			t.Errorf("Validate(%+v) = %v, want %v", test.limits, err, test.want)
		}
	}
}

func TestValidateDefaultFileCount(t *testing.T) {
	entry := exampleEntry()
	entry.Files = nil

	for index := range DefaultLimits().Files + 1 {
		name := "f" + string(rune('a'+index/26)) + string(rune('a'+index%26))
		entry.Files = append(entry.Files, File{Name: name, SHA256: exampleDigest})
	}

	if err := entry.Validate(Limits{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Validate = %v, want ErrTooLarge", err)
	}
	entry.Files = entry.Files[1:]
	if err := entry.Validate(Limits{}); err != nil {
		t.Fatalf("Validate = %v, want nil", err)
	}
}
