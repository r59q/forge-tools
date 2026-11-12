package symbols

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// goSource generates valid Go of at least the given size: numbered types
// with methods that call each other.
func goSource(size int) []byte {
	var source strings.Builder
	source.WriteString("package generated\n\nimport \"fmt\"\n")

	for index := 0; source.Len() < size; index++ {
		fmt.Fprintf(&source, `
// Record%[1]d holds one generated value.
type Record%[1]d struct {
	ID    int
	Label string
	Next  *Record%[1]d
}

// Describe returns a short text for the record.
func (r *Record%[1]d) Describe(prefix string) string {
	if r.Next != nil {
		return prefix + r.Next.Describe(prefix)
	}
	return fmt.Sprintf("%%s%%d:%%s", prefix, r.ID, normalize%[1]d(r.Label))
}

func normalize%[1]d(label string) string {
	result := make([]byte, 0, len(label))
	for position := range len(label) {
		if label[position] != ' ' {
			result = append(result, label[position])
		}
	}
	return string(result)
}
`, index)
	}
	return []byte(source.String())
}

// BenchmarkAnalyze measures one warm Analyze call per file size. The library
// README records the results.
func BenchmarkAnalyze(b *testing.B) {
	sizes := []struct {
		name  string
		bytes int
	}{
		{"small-4KiB", 4 << 10},
		{"medium-64KiB", 64 << 10},
		{"large-1MiB", 1<<20 - 1024},
	}

	analyzer := newAnalyzer(b, Limits{})
	for _, size := range sizes {
		file := File{Path: "generated.go", Content: goSource(size.bytes)}
		if len(file.Content) > 1<<20 {
			b.Fatalf("%s source has %d bytes, over the default file limit", size.name, len(file.Content))
		}

		b.Run(size.name, func(b *testing.B) {
			b.SetBytes(int64(len(file.Content)))
			b.ReportAllocs()

			for b.Loop() {
				if _, err := analyzer.Analyze(context.Background(), file); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkStart measures the first Analyze call of a new analyzer, which
// compiles the grammar module.
func BenchmarkStart(b *testing.B) {
	file := File{Path: "generated.go", Content: goSource(1 << 10)}

	for b.Loop() {
		analyzer := New(Limits{})
		if _, err := analyzer.Analyze(context.Background(), file); err != nil {
			b.Fatal(err)
		}
		if err := analyzer.Close(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

// The generated benchmark input must stay valid Go, or the numbers would
// measure error recovery.
func TestBenchmarkSourceIsValid(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{})
	analysis := analyze(t, analyzer, File{Path: "generated.go", Content: goSource(8 << 10)})

	records := 0
	for _, definition := range analysis.Definitions {
		if definition.Kind == "struct" {
			records++
		}
	}
	if want := len(analysis.Definitions) / 6; records == 0 || records != want {
		t.Errorf("got %d structs in %d definitions, want %d", records, len(analysis.Definitions), want)
	}
}
