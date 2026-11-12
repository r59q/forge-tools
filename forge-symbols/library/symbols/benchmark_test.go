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

// javaSource generates valid Java of at least the given size: numbered
// classes with methods that call each other.
func javaSource(size int) []byte {
	var source strings.Builder
	source.WriteString("package generated;\n\nimport java.util.List;\n")

	for index := 0; source.Len() < size; index++ {
		fmt.Fprintf(&source, `
/** Holds one generated value. */
final class Record%[1]d {
    private final int id;
    private final String label;
    private Record%[1]d next;

    Record%[1]d(int id, String label) {
        this.id = id;
        this.label = label;
    }

    /** Returns a short text for the record. */
    String describe(String prefix) {
        if (next != null) {
            return prefix + next.describe(prefix);
        }
        return String.format("%%s%%d:%%s", prefix, id, normalize(label));
    }

    static String normalize(String label) {
        StringBuilder result = new StringBuilder(label.length());
        for (int position = 0; position < label.length(); position++) {
            if (label.charAt(position) != ' ') {
                result.append(label.charAt(position));
            }
        }
        return result.toString();
    }
}
`, index)
	}
	return []byte(source.String())
}

// scriptSource returns a generator of valid JavaScript, or TypeScript when
// typed, of at least the given size: numbered classes with methods that
// call each other.
func scriptSource(typed bool) func(size int) []byte {
	return func(size int) []byte {
		var source strings.Builder
		source.WriteString("import { format } from \"./format\";\n")

		for index := 0; source.Len() < size; index++ {
			number, text, next := "", "", ""
			if typed {
				number, text, next = ": number", ": string", fmt.Sprintf(": Record%d | null", index)
			}
			fmt.Fprintf(&source, `
/** Holds one generated value. */
export class Record%[1]d {
  id%[2]s;
  label%[3]s;
  next%[4]s = null;

  constructor(id%[2]s, label%[3]s) {
    this.id = id;
    this.label = label;
  }

  /** Returns a short text for the record. */
  describe(prefix%[3]s)%[3]s {
    if (this.next !== null) {
      return prefix + this.next.describe(prefix);
    }
    return format(prefix, this.id, normalize%[1]d(this.label));
  }
}

export function normalize%[1]d(label%[3]s)%[3]s {
  let result = "";
  for (let position = 0; position < label.length; position++) {
    if (label[position] !== " ") {
      result += label.charAt(position);
    }
  }
  return result;
}
`, index, number, text, next)
		}
		return []byte(source.String())
	}
}

// generated lists, per language, a file name, its source generator, and
// the kind and number of definitions of each generated record.
var generated = []struct {
	language  string
	path      string
	source    func(size int) []byte
	record    string
	perRecord int
}{
	{"go", "generated.go", goSource, "struct", 6},
	{"java", "Generated.java", javaSource, "class", 7},
	{"javascript", "generated.js", scriptSource(false), "class", 7},
	{"typescript", "generated.ts", scriptSource(true), "class", 7},
}

// BenchmarkAnalyze measures one warm Analyze call per language and file
// size. The library README records the results.
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
	for _, language := range generated {
		for _, size := range sizes {
			benchmarkAnalyze(b, analyzer, language.language+"/"+size.name,
				File{Path: language.path, Content: language.source(size.bytes)})
		}
	}
}

func benchmarkAnalyze(b *testing.B, analyzer *Analyzer, name string, file File) {
	if len(file.Content) > 1<<20 {
		b.Fatalf("%s source has %d bytes, over the default file limit", name, len(file.Content))
	}

	b.Run(name, func(b *testing.B) {
		b.SetBytes(int64(len(file.Content)))
		b.ReportAllocs()

		for b.Loop() {
			if _, err := analyzer.Analyze(context.Background(), file); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkStart measures the first Analyze call of a new analyzer, which
// compiles the grammar module.
func BenchmarkStart(b *testing.B) {
	for _, language := range generated {
		file := File{Path: language.path, Content: language.source(1 << 10)}

		b.Run(language.language, func(b *testing.B) {
			for b.Loop() {
				analyzer := New(Limits{})
				if _, err := analyzer.Analyze(context.Background(), file); err != nil {
					b.Fatal(err)
				}
				if err := analyzer.Close(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// The generated benchmark input must stay valid, or the numbers would
// measure error recovery.
func TestBenchmarkSourceIsValid(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{})

	for _, language := range generated {
		analysis := analyze(t, analyzer, File{Path: language.path, Content: language.source(8 << 10)})

		records := 0
		for _, definition := range analysis.Definitions {
			if definition.Kind == language.record {
				records++
			}
		}
		if want := len(analysis.Definitions) / language.perRecord; records == 0 || records != want {
			t.Errorf("%s: got %d of kind %s in %d definitions, want %d",
				language.language, records, language.record, len(analysis.Definitions), want)
		}
	}
}
