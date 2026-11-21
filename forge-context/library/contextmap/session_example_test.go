package contextmap_test

import (
	"encoding/json"
	"os"

	"github.com/r59q/forge-tools/forge-context/library/contextmap"
)

func ExampleSession() {
	file := contextmap.NodeID{Kind: contextmap.KindFile, Repository: "shop", Path: "billing.md"}
	billing := contextmap.NodeID{Kind: contextmap.KindConcept, Concept: "billing"}
	graph, err := contextmap.NewGraph(
		[]contextmap.Node{{ID: file, Label: "Billing guide"}, {ID: billing, Label: "Billing"}},
		[]contextmap.Link{{Kind: contextmap.LinkSupportedBy, From: billing, To: file}},
	)
	if err != nil {
		panic(err)
	}

	// A chat consumer maps the events it pages or streams to these observations.
	// If the agent omits a file or concept, supply the graph ID and activity
	// from other evidence, such as a caller-tracked edit or an attached concept.
	// Omit an observation if you have no evidence for it.
	turns := [][]contextmap.Observation{
		{{ID: file, Activity: contextmap.ActivityConsidered}},
		{
			{ID: file, Activity: contextmap.ActivityRead},
			{ID: file, Activity: contextmap.ActivityTouched},
			{ID: billing, Activity: contextmap.ActivityIncluded},
		},
	}
	session := contextmap.NewSession(graph)
	encoder := json.NewEncoder(os.Stdout)
	for _, turn := range turns {
		if err := session.Record(turn...); err != nil {
			panic(err)
		}
		if err := encoder.Encode(session.Snapshot()); err != nil {
			panic(err)
		}
	}

	// Output:
	// {"Nodes":[{"ID":{"Kind":"file","Repository":"shop","Path":"billing.md","Lines":{"Start":0,"End":0},"Concept":""},"Label":"Billing guide","Activities":["considered"]}],"Links":[]}
	// {"Nodes":[{"ID":{"Kind":"file","Repository":"shop","Path":"billing.md","Lines":{"Start":0,"End":0},"Concept":""},"Label":"Billing guide","Activities":["considered","read","touched"]},{"ID":{"Kind":"concept","Repository":"","Path":"","Lines":{"Start":0,"End":0},"Concept":"billing"},"Label":"Billing","Activities":["included"]}],"Links":[{"Kind":"supported-by","From":{"Kind":"concept","Repository":"","Path":"","Lines":{"Start":0,"End":0},"Concept":"billing"},"To":{"Kind":"file","Repository":"shop","Path":"billing.md","Lines":{"Start":0,"End":0},"Concept":""}}]}
}
