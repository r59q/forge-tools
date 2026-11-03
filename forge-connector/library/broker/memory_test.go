package broker_test

import (
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/broker/storetest"
)

func TestMemoryStore(t *testing.T) {
	storetest.Run(t, func(*testing.T) broker.Store { return &broker.MemoryStore{} })
}
