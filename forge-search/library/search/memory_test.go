package search_test

import (
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-search/library/search/storetest"
)

func memoryStore(testing.TB) search.Store { return &search.MemoryStore{} }

func TestMemoryStore(t *testing.T) {
	storetest.Run(t, memoryStore)
}
