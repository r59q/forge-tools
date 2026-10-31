package broker_test

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
)

func ExampleNewManager() {
	ctx := context.Background()
	var store broker.MemoryStore
	var registry forgeauth.Registry
	if err := registry.Register(github.Provider{}); err != nil {
		panic(err)
	}
	if err := store.CreateForge(ctx, broker.Forge{
		ID: "github", Kind: forgeauth.GitHub, BaseURL: "https://api.github.com", Name: "GitHub",
	}); err != nil {
		panic(err)
	}
	// This example is ephemeral. For persistent stores, retain the key separately.
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	sealer, err := broker.NewAESGCMSealer(key)
	if err != nil {
		panic(err)
	}
	manager, err := broker.NewManager(&store, sealer, &registry)
	if err != nil {
		panic(err)
	}
	fmt.Println(manager)
	// Output: broker.Manager
}
