// Package treesitter runs Tree-sitter grammars compiled to WebAssembly.
//
// Each grammar module links the Tree-sitter C runtime, one grammar and a small
// shim (see ../grammars/build). Modules run sandboxed in wazero, without cgo
// and without access to files, the network or the clock.
package treesitter

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// ErrMemory reports that a parse needed more memory than the engine allows.
var ErrMemory = errors.New("analysis exceeds memory limit")

var errClosed = errors.New("tree-sitter engine is closed")

const pageBytes = 64 << 10

// Grammar is a parser module and the tag query to run on its trees.
type Grammar struct {
	Module []byte
	Query  string
}

// Engine owns the WebAssembly runtime and the languages loaded into it.
// It is safe for concurrent use.
type Engine struct {
	memoryPages uint32

	mu        sync.Mutex
	runtime   wazero.Runtime
	languages map[string]*entry
	closed    bool
}

// entry loads one language at most once.
type entry struct {
	once     sync.Once
	language *Language
	err      error
}

// New returns an engine whose parsers may each use up to memoryBytes.
// The runtime starts on first use.
func New(memoryBytes int) *Engine {
	pages := max(memoryBytes/pageBytes, 1)

	return &Engine{
		memoryPages: uint32(min(pages, 1<<16)), //nolint:gosec // bounded to the 4 GiB WebAssembly maximum.
		languages:   map[string]*entry{},
	}
}

// Language returns the named language. The first call loads its grammar and
// compiles it; later calls reuse the result.
func (e *Engine) Language(ctx context.Context, name string, load func() (Grammar, error)) (*Language, error) {
	// Loading is shared by all callers, so one caller cancelling must not fail it.
	ctx = context.WithoutCancel(ctx)

	entry, err := e.entry(ctx, name)
	if err != nil {
		return nil, err
	}

	entry.once.Do(func() {
		entry.language, entry.err = e.load(ctx, load)
	})
	return entry.language, entry.err
}

// Close stops the runtime and every parser. Running calls fail.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.closed = true
	if e.runtime == nil {
		return nil
	}
	return e.runtime.Close(ctx)
}

func (e *Engine) entry(ctx context.Context, name string) (*entry, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return nil, errClosed
	}
	if err := e.start(ctx); err != nil {
		return nil, err
	}

	if e.languages[name] == nil {
		e.languages[name] = &entry{}
	}
	return e.languages[name], nil
}

// start creates the runtime with the host functions grammar modules import.
func (e *Engine) start(ctx context.Context) error {
	if e.runtime != nil {
		return nil
	}

	config := wazero.NewRuntimeConfig().WithMemoryLimitPages(e.memoryPages)
	runtime := wazero.NewRuntimeWithConfig(ctx, config)

	if err := addImports(ctx, runtime); err != nil {
		return errors.Join(fmt.Errorf("start tree-sitter runtime: %w", err), runtime.Close(ctx))
	}

	e.runtime = runtime
	return nil
}

func addImports(ctx context.Context, runtime wazero.Runtime) error {
	// WASI is only linked for the C library. The modules get no files,
	// arguments, environment, output or real clock.
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		return err
	}

	_, err := runtime.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithGoFunction(api.GoFunc(shouldStop), nil, []api.ValueType{api.ValueTypeI32}).
		Export("should_stop").
		Instantiate(ctx)
	return err
}

// shouldStop tells a parser whether the context of its running call ended.
func shouldStop(ctx context.Context, stack []uint64) {
	stack[0] = 0
	if ctx.Err() != nil {
		stack[0] = 1
	}
}

func (e *Engine) load(ctx context.Context, load func() (Grammar, error)) (*Language, error) {
	grammar, err := load()
	if err != nil {
		return nil, err
	}

	compiled, err := e.runtime.CompileModule(ctx, grammar.Module)
	if err != nil {
		return nil, fmt.Errorf("compile grammar module: %w", err)
	}

	return newLanguage(ctx, e.runtime, compiled, grammar.Query)
}
