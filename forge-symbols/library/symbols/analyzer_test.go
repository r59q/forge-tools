package symbols

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const sample = `package sample

type Greeter struct{}

func (g Greeter) Greet(name string) string { return format(name) }

func format(name string) string { return name }
`

const javaSample = `class Greeter {
    Greeter() {}

    String greet(String name) { return format(name); }
}
`

const javascriptSample = `class Greeter {
  greet(name) { return <Text>{name}</Text>; }
}
`

const typescriptSample = `interface Greeter {
  greet(name: string): string;
}
`

const tsxSample = `interface Greeter {
  greet(name: string): string;
}

const render = <T,>(value: T) => <Text>{value}</Text>;
`

const pythonSample = `class Greeter:
    def greet(self, name):
        return format(name)


def format(name):
    return name
`

// samples holds, per language, a source file and the definitions in it.
var samples = map[string]struct{ source, definitions string }{
	"go":         {sample, "struct Greeter, method Greet, function format"},
	"java":       {javaSample, "class Greeter, constructor Greeter, method greet"},
	"javascript": {javascriptSample, "class Greeter, method greet"},
	"typescript": {typescriptSample, "interface Greeter, method greet"},
	"tsx":        {tsxSample, "interface Greeter, method greet, function render"},
	"python":     {pythonSample, "class Greeter, method greet, function format"},
}

func analyze(t *testing.T, analyzer *Analyzer, file File) Analysis {
	t.Helper()

	analysis, err := analyzer.Analyze(context.Background(), file)
	if err != nil {
		t.Fatalf("analyze %s: %v", file.Path, err)
	}
	return analysis
}

func names(definitions []Definition) string {
	var names []string
	for _, definition := range definitions {
		names = append(names, definition.Kind+" "+definition.Name)
	}
	return strings.Join(names, ", ")
}

func TestAnalyzeDetectsLanguage(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{})

	tests := []struct {
		name     string
		file     File
		language string
	}{
		{"extension", File{Path: "pkg/sample.go"}, "go"},
		{"upper-case extension", File{Path: "SAMPLE.GO"}, "go"},
		{"override without extension", File{Path: "sample", Language: "go"}, "go"},
		{"override of another extension", File{Path: "sample.txt", Language: "go"}, "go"},
		{"override without path", File{Language: "go"}, "go"},
		{"java extension", File{Path: "src/main/java/Greeter.java"}, "java"},
		{"upper-case java extension", File{Path: "GREETER.JAVA"}, "java"},
		{"java override", File{Path: "Greeter.txt", Language: "java"}, "java"},
		{"java override of go extension", File{Path: "Greeter.go", Language: "java"}, "java"},
		{"js extension", File{Path: "src/greeter.js"}, "javascript"},
		{"mjs extension", File{Path: "greeter.mjs"}, "javascript"},
		{"cjs extension", File{Path: "greeter.cjs"}, "javascript"},
		{"jsx extension", File{Path: "Greeter.JSX"}, "javascript"},
		{"ts extension", File{Path: "src/greeter.ts"}, "typescript"},
		{"mts extension", File{Path: "greeter.mts"}, "typescript"},
		{"cts extension", File{Path: "greeter.cts"}, "typescript"},
		{"tsx extension", File{Path: "Greeter.tsx"}, "tsx"},
		{"javascript override", File{Path: "greeter.txt", Language: "javascript"}, "javascript"},
		{"typescript override", File{Path: "greeter.js", Language: "typescript"}, "typescript"},
		{"tsx override of ts extension", File{Path: "greeter.ts", Language: "tsx"}, "tsx"},
		{"py extension", File{Path: "pkg/greeter.py"}, "python"},
		{"pyi stub extension", File{Path: "typeshed/greeter.PYI"}, "python"},
		{"python override", File{Path: "SConstruct", Language: "python"}, "python"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.file.Content = []byte(samples[test.language].source)
			analysis := analyze(t, analyzer, test.file)

			if analysis.Language != test.language || analysis.Path != test.file.Path {
				t.Errorf("got language %q path %q", analysis.Language, analysis.Path)
			}
			if analysis.Bytes != len(test.file.Content) {
				t.Errorf("bytes = %d, want %d", analysis.Bytes, len(test.file.Content))
			}
			if got, want := names(analysis.Definitions), samples[test.language].definitions; got != want {
				t.Errorf("definitions = %s, want %s", got, want)
			}
		})
	}
}

func TestAnalyzeUnsupportedLanguage(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{})

	files := []File{
		{Path: "secret/main.rs"},
		{Path: "Makefile"},
		{Path: "archive.go.bak"},
		{},
		{Path: "main.go", Language: "cobol"},
	}
	for _, file := range files {
		file.Content = []byte(sample)

		_, err := analyzer.Analyze(context.Background(), file)
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%+v: got %v, want ErrUnsupported", file.Path, err)
		}
		if err != nil && file.Path != "" && strings.Contains(err.Error(), file.Path) {
			t.Errorf("error %q contains the path", err)
		}
	}
}

func TestDetectLanguage(t *testing.T) {
	if language, ok := DetectLanguage("a/b/c.go"); !ok || language != "go" {
		t.Errorf("got %q, %v", language, ok)
	}
	if language, ok := DetectLanguage("src/Main.java"); !ok || language != "java" {
		t.Errorf("got %q, %v", language, ok)
	}
	if language, ok := DetectLanguage("a.go/readme"); ok {
		t.Errorf("detected %q for a path without extension", language)
	}
	if got := Languages(); !slices.Equal(got, []string{"go", "java", "javascript", "typescript", "tsx", "python"}) {
		t.Errorf("Languages() = %v", got)
	}
}

// Broken source is normal input for an editor: it gives what the recovered
// tree still shows, never an error.
func TestAnalyzeInvalidSource(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{})

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", ""},
		{"package only", "package", ""},
		{"truncated body", strings.SplitN(sample, "return format", 2)[0], "struct Greeter, method Greet"},
		{"unclosed call", "package p\n\nfunc A() { b(1, }\n\nfunc C() {}\n", "function A, function C"},
		{"stray tokens", "package p\n\n)]}} else\n\nfunc A() {}\n\ntype T struct { x int\n", "function A, field x"},
		{"not go", "<html><body>hello</body></html>", ""},
		{"binary", "\x00\xff\xfe\x80\x81func\x00", ""},
		{"invalid utf-8 in name", "package p\n\nfunc A\xff() {}\n\nfunc B() {}\n", "function A, function B"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := analyze(t, analyzer, File{Path: "broken.go", Content: []byte(test.content)})

			if got := names(analysis.Definitions); got != test.want {
				t.Errorf("definitions = %q, want %q", got, test.want)
			}
			if analysis.Definitions == nil || analysis.References == nil {
				t.Error("empty results must be empty slices, not nil")
			}
		})
	}
}

// Columns count UTF-16 code units: "é" is one, "世" is one, "😀" is two.
func TestAnalyzeNonASCIIRanges(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{})
	source := "package p\n\n" +
		"/* é世😀 */ func 名前(x T) { 呼ぶ(\"😀😀\", gé(x)) }\r\n" +
		"type Ünï struct{ fé T }\n"

	analysis := analyze(t, analyzer, File{Path: "ünï.go", Content: []byte(source)})

	definitions := []Definition{
		{Name: "名前", Kind: "function", NameRange: Range{3, 17, 3, 19}, Range: Range{3, 12, 3, 46}, Parent: None},
		{Name: "Ünï", Kind: "struct", NameRange: Range{4, 6, 4, 9}, Range: Range{4, 6, 4, 24}, Parent: None},
		{Name: "fé", Kind: "field", NameRange: Range{4, 18, 4, 20}, Range: Range{4, 18, 4, 22}, Container: "Ünï", Parent: 1},
	}
	references := []Reference{
		{Name: "T", Kind: "type", Range: Range{3, 22, 3, 23}, Enclosing: 0},
		{Name: "呼ぶ", Kind: "call", Range: Range{3, 27, 3, 29}, Enclosing: 0},
		{Name: "gé", Kind: "call", Range: Range{3, 38, 3, 40}, Enclosing: 0},
		{Name: "T", Kind: "type", Range: Range{4, 21, 4, 22}, Enclosing: 2},
	}

	if got, want := fmt.Sprintf("%+v", analysis.Definitions), fmt.Sprintf("%+v", definitions); got != want {
		t.Errorf("definitions:\n got %s\nwant %s", got, want)
	}
	if got, want := fmt.Sprintf("%+v", analysis.References), fmt.Sprintf("%+v", references); got != want {
		t.Errorf("references:\n got %s\nwant %s", got, want)
	}
}

func TestAnalysisLookups(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{})
	analysis := analyze(t, analyzer, File{
		Path:    "a.go",
		Content: []byte("package p\n\ntype T struct{ f int }\n\nvar _ = g()\n\nfunc (t T) M() { g() }\n"),
	})

	field := analysis.Definitions[1]
	if parent, ok := analysis.Parent(field); !ok || parent.Name != "T" || field.Container != "T" {
		t.Errorf("parent of %s = %+v, %v", field.Name, parent, ok)
	}

	method := analysis.Definitions[2]
	if _, ok := analysis.Parent(method); ok || method.Container != "T" {
		t.Errorf("method %+v: want container T and no parent", method)
	}

	var enclosing []string
	for _, reference := range analysis.References {
		if reference.Name != "g" {
			continue
		}
		definition, ok := analysis.Enclosing(reference)
		enclosing = append(enclosing, fmt.Sprint(definition.Name, ok))
	}
	if got := strings.Join(enclosing, ","); got != "false,Mtrue" {
		t.Errorf("enclosing definitions of g() = %s", got)
	}
}

func TestDefaultLimits(t *testing.T) {
	limits := New(Limits{}).Limits()
	if limits != DefaultLimits() {
		t.Errorf("zero limits = %+v, want the defaults", limits)
	}
	if limits.FileBytes != 1<<20 {
		t.Errorf("default file limit = %d, want 1 MiB", limits.FileBytes)
	}

	custom := New(Limits{Files: 3, MemoryBytes: 1}).Limits()
	if custom.Files != 3 || custom.MemoryBytes != MinMemoryBytes || custom.FileBytes != limits.FileBytes {
		t.Errorf("custom limits = %+v", custom)
	}
}

func TestFileSizeLimit(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{FileBytes: len(sample)})

	analyze(t, analyzer, File{Path: "fits.go", Content: []byte(sample)})

	_, err := analyzer.Analyze(context.Background(), File{Path: "big.go", Content: []byte(sample + "\n")})
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("got %v, want ErrTooLarge", err)
	}
}

func TestRequestLimits(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{Files: 2, TotalBytes: 2 * len(sample)})
	file := File{Path: "a.go", Content: []byte(sample)}

	analyses, err := analyzer.AnalyzeFiles(context.Background(), []File{file, {Path: "b.go", Content: []byte(sample)}})
	if err != nil || len(analyses) != 2 || analyses[1].Path != "b.go" {
		t.Fatalf("got %d analyses, %v", len(analyses), err)
	}

	if _, err := analyzer.AnalyzeFiles(context.Background(), []File{file, file, file}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too many files: got %v, want ErrTooLarge", err)
	}

	padded := File{Path: "c.go", Content: []byte(sample + "\n")}
	if _, err := analyzer.AnalyzeFiles(context.Background(), []File{file, padded}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too many bytes: got %v, want ErrTooLarge", err)
	}

	if analyses, err := analyzer.AnalyzeFiles(context.Background(), nil); err != nil || len(analyses) != 0 {
		t.Errorf("empty request: got %v, %v", analyses, err)
	}
}

func TestAnalyzeFilesReportsFailingFile(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{})
	files := []File{{Path: "a.go", Content: []byte(sample)}, {Path: "private/notes.txt"}}

	_, err := analyzer.AnalyzeFiles(context.Background(), files)
	if !errors.Is(err, ErrUnsupported) || !strings.HasPrefix(err.Error(), "file 1: ") {
		t.Errorf("got %v, want ErrUnsupported for file 1", err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Errorf("error %q contains the path", err)
	}
}

// pathological is input the parser cannot finish in reasonable time or memory.
func pathological() []byte {
	return bytes.Repeat([]byte("("), 1<<20)
}

func TestParseTimeLimit(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{ParseTime: 50 * time.Millisecond})
	analyze(t, analyzer, File{Path: "warm.go", Content: []byte(sample)})

	start := time.Now()
	_, err := analyzer.Analyze(context.Background(), File{Path: "slow.go", Content: pathological()})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("stopped after %s, want close to the 50ms limit", elapsed)
	}

	// The analyzer keeps working after a parse was stopped.
	analyze(t, analyzer, File{Path: "after.go", Content: []byte(sample)})
}

func TestContextEnds(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := analyzer.Analyze(cancelled, File{Path: "a.go", Content: []byte(sample)}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: got %v, want context.Canceled", err)
	}

	expiring, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := analyzer.Analyze(expiring, File{Path: "slow.go", Content: pathological()})
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTimeout) {
		t.Errorf("expired context: got %v, want context.DeadlineExceeded", err)
	}

	stopping, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	if _, err := analyzer.Analyze(stopping, File{Path: "slow.go", Content: pathological()}); !errors.Is(err, context.Canceled) {
		t.Errorf("context cancelled while parsing: got %v, want context.Canceled", err)
	}
}

func TestMemoryLimit(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{MemoryBytes: 8 << 20, ParseTime: time.Minute})

	_, err := analyzer.Analyze(context.Background(), File{Path: "deep.go", Content: pathological()})
	if !errors.Is(err, ErrMemory) {
		t.Fatalf("got %v, want ErrMemory", err)
	}

	analyze(t, analyzer, File{Path: "after.go", Content: []byte(sample)})
}

func TestMinimumMemoryStartsParser(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{MemoryBytes: 1})
	analyze(t, analyzer, File{Path: "a.go", Content: []byte(sample)})
}

func TestAnalyzeConcurrently(t *testing.T) {
	analyzer := newAnalyzer(t, Limits{})
	want := render(analyze(t, analyzer, File{Path: "a.go", Content: []byte(sample)}))

	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			for range 8 {
				analysis, err := analyzer.Analyze(context.Background(), File{Path: "a.go", Content: []byte(sample)})
				if err != nil {
					t.Error(err)
					return
				}
				if got := render(analysis); got != want {
					t.Errorf("concurrent result differs:\n%s", got)
				}
			}
		})
	}
	group.Wait()
}

func TestAnalyzeAfterClose(t *testing.T) {
	analyzer := New(Limits{})
	if _, err := analyzer.Analyze(context.Background(), File{Path: "a.go", Content: []byte(sample)}); err != nil {
		t.Fatal(err)
	}
	if err := analyzer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := analyzer.Analyze(context.Background(), File{Path: "a.go", Content: []byte(sample)}); err == nil {
		t.Error("analyze after close succeeded")
	}
}
