// Package grammars bundles the Tree-sitter grammars and tag queries.
//
// Each language has a <name>.wasm parser module, built by build/build.sh,
// and a <name>.scm tag query. Nothing is loaded from analyzed input.
package grammars

import "embed"

//go:embed *.wasm *.scm
var files embed.FS

// Language is one bundled language.
type Language struct {
	// Name identifies the language and names its two files.
	Name string

	// Extensions are the lower-case file extensions, with the dot.
	Extensions []string
}

// Languages lists the bundled languages. To add one, add a row here, build
// its module with build/build.sh and write its tag query.
func Languages() []Language {
	return []Language{
		{Name: "go", Extensions: []string{".go"}},
		{Name: "java", Extensions: []string{".java"}},
	}
}

// Module returns the WebAssembly parser module of the language.
func (l Language) Module() ([]byte, error) {
	return files.ReadFile(l.Name + ".wasm")
}

// Query returns the tag query of the language.
func (l Language) Query() (string, error) {
	query, err := files.ReadFile(l.Name + ".scm")
	return string(query), err
}
