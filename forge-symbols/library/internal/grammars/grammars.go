// Package grammars bundles the Tree-sitter grammars and tag queries.
//
// Each language has a <name>.wasm parser module, built by build/build.sh,
// and a tag query joined from one or more <query>.scm files, so related
// grammars share query files. Nothing is loaded from analyzed input.
package grammars

import (
	"embed"
	"strings"
)

//go:embed *.wasm *.scm
var files embed.FS

// Language is one bundled language.
type Language struct {
	// Name identifies the language and names its parser module.
	Name string

	// Extensions are the lower-case file extensions, with the dot.
	Extensions []string

	// Queries name the .scm files of the tag query, joined in order. When
	// patterns tag the same name, the first one wins.
	Queries []string
}

// Languages lists the bundled languages. To add one, add a row here, build
// its module with build/build.sh and write its tag query.
func Languages() []Language {
	return []Language{
		{Name: "go", Extensions: []string{".go"}, Queries: []string{"go"}},
		{Name: "java", Extensions: []string{".java"}, Queries: []string{"java"}},
		{
			Name:       "javascript",
			Extensions: []string{".js", ".mjs", ".cjs", ".jsx"},
			Queries:    []string{"ecmascript", "javascript", "jsx"},
		},
		{
			Name:       "typescript",
			Extensions: []string{".ts", ".mts", ".cts"},
			Queries:    []string{"ecmascript", "typescript"},
		},
		{
			Name:       "tsx",
			Extensions: []string{".tsx"},
			Queries:    []string{"ecmascript", "typescript", "jsx"},
		},
	}
}

// Module returns the WebAssembly parser module of the language.
func (l Language) Module() ([]byte, error) {
	return files.ReadFile(l.Name + ".wasm")
}

// Query returns the tag query of the language.
func (l Language) Query() (string, error) {
	parts := make([]string, 0, len(l.Queries))

	for _, name := range l.Queries {
		part, err := files.ReadFile(name + ".scm")
		if err != nil {
			return "", err
		}
		parts = append(parts, string(part))
	}
	return strings.Join(parts, "\n"), nil
}
