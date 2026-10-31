package broker_test

import (
	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/broker/storetest"
	"testing"
)

func TestMemoryStore(t *testing.T) {
	storetest.Run(t, func(*testing.T) broker.Store { return &broker.MemoryStore{} })
}
