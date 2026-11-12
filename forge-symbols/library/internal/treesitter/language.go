package treesitter

import (
	"context"
	"runtime"

	"github.com/tetratelabs/wazero"
)

// Parsers that grew beyond this are closed instead of pooled, so one large
// file does not pin its memory.
const pooledMemoryBytes = 32 << 20

// Capture is one captured node of a match, as a byte range of the source.
type Capture struct {
	Name  string
	Start int
	End   int
}

// Match holds the captures of one tag query match.
type Match struct {
	Captures []Capture
}

// Language parses one language. It is safe for concurrent use: each call
// runs in its own parser, and idle parsers are pooled.
type Language struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	source   string
	query    *query
	pool     chan *instance
}

func newLanguage(ctx context.Context, runtime wazero.Runtime, compiled wazero.CompiledModule, source string) (*Language, error) {
	language := &Language{
		runtime:  runtime,
		compiled: compiled,
		source:   source,
		pool:     make(chan *instance, poolSize()),
	}

	first, err := language.instantiate(ctx)
	if err != nil {
		return nil, err
	}

	language.query, err = first.describe(ctx)
	language.recycle(ctx, first, err)
	if err != nil {
		return nil, err
	}
	return language, nil
}

func poolSize() int {
	return runtime.GOMAXPROCS(0)
}

// Matches parses source and returns the tag query matches that pass their
// predicates. Invalid source still yields the matches of the recovered tree.
//
// It returns ctx.Err() when ctx ends first, and ErrMemory when the parse
// exceeds the memory limit.
func (l *Language) Matches(ctx context.Context, source []byte) ([]Match, error) {
	instance, err := l.acquire(ctx)
	if err != nil {
		return nil, err
	}

	matches, err := instance.matches(ctx, l.query, source)
	l.recycle(ctx, instance, err)
	return matches, err
}

func (l *Language) acquire(ctx context.Context) (*instance, error) {
	select {
	case instance := <-l.pool:
		return instance, nil
	default:
		return l.instantiate(ctx)
	}
}

// recycle pools a healthy parser and closes a failed, large or surplus one.
func (l *Language) recycle(ctx context.Context, instance *instance, err error) {
	if err != nil || instance.memoryBytes() > pooledMemoryBytes {
		instance.close(ctx)
		return
	}

	select {
	case l.pool <- instance:
	default:
		instance.close(ctx)
	}
}
