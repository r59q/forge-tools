package contextmap_test

import (
	"fmt"

	"github.com/r59q/forge-tools/forge-context/library/contextmap"
)

func ExampleSession_ExpandedView() {
	checkout := contextmap.NodeID{Kind: contextmap.KindFile, Repository: "checkout", Path: "docs/refunds.md"}
	ledger := contextmap.NodeID{Kind: contextmap.KindFile, Repository: "ledger", Path: "refunds.md"}
	graph, err := contextmap.DiscoverConcepts(map[contextmap.NodeID]string{
		checkout: "# Refunds\nHow to request a refund.",
		ledger:   "# Refunds\nHow to reconcile a refund.",
	})
	if err != nil {
		panic(err)
	}

	session := contextmap.NewSession(graph)
	if err := session.Record(contextmap.Observation{ID: checkout, Activity: contextmap.ActivityRead}); err != nil {
		panic(err)
	}

	// The operator expands the file the agent read.
	view, found := session.ExpandedView(checkout)
	if !found {
		panic("checkout guide missing")
	}
	for _, node := range view.Nodes {
		label := node.Label
		if node.ID.Kind == contextmap.KindFile {
			label = node.ID.Repository + "/" + node.ID.Path
		}
		fmt.Println(node.ID.Kind, label, node.Activities)
	}

	// Output:
	// file checkout/docs/refunds.md [read]
	// concept Refunds []
	// file ledger/refunds.md []
}
