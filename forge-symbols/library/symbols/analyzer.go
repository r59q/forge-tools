package symbols

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/r59q/forge-tools/forge-symbols/library/internal/grammars"
	"github.com/r59q/forge-tools/forge-symbols/library/internal/treesitter"
)

// Analyzer finds definitions and references in source files. It is safe for
// concurrent use. Parsers start on first use and are reused; call Close to
// free them.
type Analyzer struct {
	limits Limits
	engine *treesitter.Engine
}

// New returns an analyzer with the given limits. Zero fields use the default.
func New(limits Limits) *Analyzer {
	limits = limits.withDefaults()

	return &Analyzer{
		limits: limits,
		engine: treesitter.New(limits.MemoryBytes),
	}
}

// Limits returns the limits in effect.
func (a *Analyzer) Limits() Limits {
	return a.limits
}

// Close frees the parsers. The analyzer cannot be used afterwards.
func (a *Analyzer) Close(ctx context.Context) error {
	return a.engine.Close(ctx)
}

// Languages returns the names of the supported languages.
func Languages() []string {
	var names []string
	for _, language := range grammars.Languages() {
		names = append(names, language.Name)
	}
	return names
}

// DetectLanguage returns the language of a path from its file extension.
func DetectLanguage(filePath string) (string, bool) {
	extension := strings.ToLower(path.Ext(filePath))

	for _, language := range grammars.Languages() {
		for _, candidate := range language.Extensions {
			if candidate == extension {
				return language.Name, true
			}
		}
	}
	return "", false
}

// Analyze returns the definitions and references of one file. Invalid or
// partial source gives best-effort results, not an error.
//
// It returns ErrUnsupported for an unknown language, ErrTooLarge for content
// over Limits.FileBytes, ErrTimeout after Limits.ParseTime, ErrMemory over
// Limits.MemoryBytes, and ctx.Err() when ctx ends first.
func (a *Analyzer) Analyze(ctx context.Context, file File) (Analysis, error) {
	language, err := resolveLanguage(file)
	if err != nil {
		return Analysis{}, err
	}
	if len(file.Content) > a.limits.FileBytes {
		return Analysis{}, fmt.Errorf("%w: file has %d bytes, limit is %d", ErrTooLarge, len(file.Content), a.limits.FileBytes)
	}

	matches, err := a.matches(ctx, language, file.Content)
	if err != nil {
		return Analysis{}, err
	}

	analysis := newAnalysis(matches, file.Content)
	analysis.Path = file.Path
	analysis.Language = language.Name
	return analysis, nil
}

// AnalyzeFiles analyzes the files of one request in order and returns one
// analysis per file. It returns ErrTooLarge for more than Limits.Files files
// or Limits.TotalBytes of content, and stops at the first file that fails.
func (a *Analyzer) AnalyzeFiles(ctx context.Context, files []File) ([]Analysis, error) {
	if err := a.checkRequest(files); err != nil {
		return nil, err
	}

	analyses := make([]Analysis, 0, len(files))
	for index, file := range files {
		analysis, err := a.Analyze(ctx, file)
		if err != nil {
			return nil, fmt.Errorf("file %d: %w", index, err)
		}
		analyses = append(analyses, analysis)
	}
	return analyses, nil
}

func (a *Analyzer) checkRequest(files []File) error {
	if len(files) > a.limits.Files {
		return fmt.Errorf("%w: request has %d files, limit is %d", ErrTooLarge, len(files), a.limits.Files)
	}

	total := 0
	for _, file := range files {
		total += len(file.Content)
	}
	if total > a.limits.TotalBytes {
		return fmt.Errorf("%w: request has %d bytes, limit is %d", ErrTooLarge, total, a.limits.TotalBytes)
	}
	return nil
}

func resolveLanguage(file File) (grammars.Language, error) {
	name := file.Language
	if name == "" {
		name, _ = DetectLanguage(file.Path)
	}

	for _, language := range grammars.Languages() {
		if language.Name == name && name != "" {
			return language, nil
		}
	}
	return grammars.Language{}, ErrUnsupported
}

// matches runs the tag query of the language within the parse time limit.
func (a *Analyzer) matches(ctx context.Context, language grammars.Language, content []byte) ([]treesitter.Match, error) {
	parser, err := a.engine.Language(ctx, language.Name, func() (treesitter.Grammar, error) {
		return loadGrammar(language)
	})
	if err != nil {
		return nil, fmt.Errorf("load %s grammar: %w", language.Name, err)
	}

	limited, cancel := context.WithTimeout(ctx, a.limits.ParseTime)
	defer cancel()

	matches, err := parser.Matches(limited, content)
	switch {
	case err == nil:
		return matches, nil
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return nil, fmt.Errorf("%w of %s", ErrTimeout, a.limits.ParseTime)
	}
	return nil, err
}

func loadGrammar(language grammars.Language) (treesitter.Grammar, error) {
	module, err := language.Module()
	if err != nil {
		return treesitter.Grammar{}, err
	}

	query, err := language.Query()
	if err != nil {
		return treesitter.Grammar{}, err
	}
	return treesitter.Grammar{Module: module, Query: query}, nil
}
