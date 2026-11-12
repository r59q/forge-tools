package treesitter

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Functions exported by the shim in ../grammars/build/shim.c.
const (
	exportAlloc     = "sym_alloc"
	exportFree      = "sym_free"
	exportInit      = "sym_init"
	exportInfo      = "sym_info"
	exportAnalyze   = "sym_analyze"
	exportExhausted = "sym_exhausted"
)

// instance is one parser: an instantiated grammar module with its compiled
// query. It serves one call at a time.
type instance struct {
	module api.Module
}

func (l *Language) instantiate(ctx context.Context) (*instance, error) {
	ctx = context.WithoutCancel(ctx)
	config := wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize")

	module, err := l.runtime.InstantiateModule(ctx, l.compiled, config)
	if err != nil {
		return nil, fmt.Errorf("start grammar module: %w", err)
	}

	instance := &instance{module: module}
	if err := instance.init(ctx, l.source); err != nil {
		instance.close(ctx)
		return nil, err
	}
	return instance, nil
}

// init compiles the tag query inside the module.
func (i *instance) init(ctx context.Context, source string) error {
	pointer, err := i.write(ctx, []byte(source))
	if err != nil {
		return err
	}

	failure, err := i.call(ctx, exportInit, uint64(pointer), uint64(len(source)))
	if err != nil {
		return err
	}
	if failure != 0 {
		return fmt.Errorf("compile tag query: error %d at offset %d", failure>>32, failure&math.MaxUint32)
	}

	_, err = i.call(ctx, exportFree, uint64(pointer))
	return err
}

// describe reads the capture names and predicates of the compiled query.
func (i *instance) describe(ctx context.Context) (*query, error) {
	span, err := i.call(ctx, exportInfo)
	if err != nil {
		return nil, err
	}

	info, err := i.read(span)
	if err != nil {
		return nil, err
	}
	return parseQuery(info)
}

func (i *instance) matches(ctx context.Context, query *query, source []byte) ([]Match, error) {
	pointer, err := i.write(ctx, source)
	if err != nil {
		return nil, err
	}

	span, err := i.call(ctx, exportAnalyze, uint64(pointer), uint64(len(source)))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	records, err := i.read(span)
	if err != nil {
		return nil, err
	}
	matches, err := query.decode(records, source)
	if err != nil {
		return nil, err
	}

	_, err = i.call(ctx, exportFree, uint64(pointer))
	return matches, err
}

// write copies bytes into module memory and returns their address.
func (i *instance) write(ctx context.Context, bytes []byte) (uint32, error) {
	if len(bytes) > math.MaxUint32 {
		return 0, ErrMemory
	}

	address, err := i.call(ctx, exportAlloc, uint64(len(bytes)))
	if err != nil {
		return 0, err
	}

	pointer := uint32(address) //nolint:gosec // module addresses are 32-bit.
	if pointer == 0 || !i.module.Memory().Write(pointer, bytes) {
		return 0, ErrMemory
	}
	return pointer, nil
}

// read returns the module memory a shim function described as address and
// length. The view is only valid until the next call.
func (i *instance) read(span uint64) ([]byte, error) {
	view, ok := i.module.Memory().Read(uint32(span>>32), uint32(span)) //nolint:gosec // two packed 32-bit words.
	if span == 0 || !ok {
		return nil, errors.New("grammar module returned no result")
	}
	return view, nil
}

func (i *instance) call(ctx context.Context, name string, arguments ...uint64) (uint64, error) {
	function := i.module.ExportedFunction(name)
	if function == nil {
		return 0, fmt.Errorf("grammar module lacks %s", name)
	}

	results, err := function.Call(ctx, arguments...)
	if err != nil {
		return 0, i.failure(ctx, name, err)
	}
	if len(results) == 0 {
		return 0, nil
	}
	return results[0], nil
}

// failure explains a trapped call. The shim traps when memory runs out.
func (i *instance) failure(ctx context.Context, name string, cause error) error {
	exhausted := i.module.ExportedFunction(exportExhausted)
	if exhausted != nil {
		results, err := exhausted.Call(context.WithoutCancel(ctx))
		if err == nil && len(results) == 1 && results[0] != 0 {
			return ErrMemory
		}
	}
	return fmt.Errorf("%s: %w", name, cause)
}

func (i *instance) memoryBytes() uint32 {
	return i.module.Memory().Size()
}

func (i *instance) close(ctx context.Context) {
	// Closing only frees the sandbox; there is nothing to report or recover.
	_ = i.module.Close(context.WithoutCancel(ctx))
}
