package grammars_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-symbols/library/internal/grammars"
	"github.com/r59q/forge-tools/forge-symbols/library/internal/treesitter"
)

// Every bundled language must ship a parser module and a tag query that
// load together, and must own its file extensions.
func TestBundledLanguagesLoad(t *testing.T) {
	engine := treesitter.New(64 << 20)
	t.Cleanup(func() {
		if err := engine.Close(context.Background()); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	owners := map[string]string{}
	for _, language := range grammars.Languages() {
		module, err := language.Module()
		if err != nil || !bytes.HasPrefix(module, []byte("\x00asm")) {
			t.Errorf("%s: module is not WebAssembly (%v)", language.Name, err)
		}

		query, err := language.Query()
		if err != nil || !strings.Contains(query, "@name") {
			t.Errorf("%s: query has no @name capture (%v)", language.Name, err)
		}

		_, err = engine.Language(context.Background(), language.Name, func() (treesitter.Grammar, error) {
			return treesitter.Grammar{Module: module, Query: query}, nil
		})
		if err != nil {
			t.Errorf("%s: %v", language.Name, err)
		}

		if len(language.Extensions) == 0 {
			t.Errorf("%s: no file extensions", language.Name)
		}
		for _, extension := range language.Extensions {
			if extension != strings.ToLower(extension) || !strings.HasPrefix(extension, ".") {
				t.Errorf("%s: extension %q must be lower case and start with a dot", language.Name, extension)
			}
			if owner, taken := owners[extension]; taken {
				t.Errorf("extension %s belongs to both %s and %s", extension, owner, language.Name)
			}
			owners[extension] = language.Name
		}
	}
}
