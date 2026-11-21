package contextmap_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/r59q/forge-tools/forge-context/library/contextmap"
)

func TestSessionTracksDistinctFileActivities(t *testing.T) {
	file := sessionFile("billing.md")
	session := contextmap.NewSession(makeGraph(t, []contextmap.Node{file}, nil))
	read := contextmap.Observation{ID: file.ID, Activity: contextmap.ActivityRead}
	considered := contextmap.Observation{ID: file.ID, Activity: contextmap.ActivityConsidered}
	touched := contextmap.Observation{ID: file.ID, Activity: contextmap.ActivityTouched}

	recordSession(t, session, read, considered)
	first := session.Snapshot()
	recordSession(t, session, touched, read)

	checkSessionNodes(t, first, contextmap.SessionNode{
		Node:       file,
		Activities: []contextmap.Activity{contextmap.ActivityRead, contextmap.ActivityConsidered},
	})
	first.Nodes[0].Label = "changed by consumer"
	first.Nodes[0].Activities[0] = contextmap.ActivityTouched
	checkSessionNodes(t, session.Snapshot(), contextmap.SessionNode{
		Node:       file,
		Activities: []contextmap.Activity{contextmap.ActivityRead, contextmap.ActivityConsidered, contextmap.ActivityTouched},
	})
}

func TestSessionsShareGraphWithoutSharingActivity(t *testing.T) {
	file := sessionFile("billing.md")
	evidence := sessionFile("invoice.md")
	unrelated := concept("tax")
	billing := concept("billing")
	links := []contextmap.Link{
		{Kind: contextmap.LinkSupportedBy, From: billing.ID, To: file.ID},
		{Kind: contextmap.LinkSupportedBy, From: billing.ID, To: evidence.ID},
		{Kind: contextmap.LinkSupportedBy, From: unrelated.ID, To: evidence.ID},
	}
	graph := makeGraph(t, []contextmap.Node{file, evidence, billing, unrelated}, links)
	first, second := contextmap.NewSession(graph), contextmap.NewSession(graph)
	recordSession(t, first,
		contextmap.Observation{ID: file.ID, Activity: contextmap.ActivityRead},
		contextmap.Observation{ID: billing.ID, Activity: contextmap.ActivityIncluded},
	)
	recordSession(t, second, contextmap.Observation{ID: file.ID, Activity: contextmap.ActivityTouched})

	snapshot := first.Snapshot()
	checkSessionNodes(t, snapshot,
		contextmap.SessionNode{Node: file, Activities: []contextmap.Activity{contextmap.ActivityRead}},
		contextmap.SessionNode{Node: evidence},
		contextmap.SessionNode{Node: billing, Activities: []contextmap.Activity{contextmap.ActivityIncluded}},
	)
	if !reflect.DeepEqual(snapshot.Links, links[:2]) {
		t.Errorf("first session links = %+v, want %+v", snapshot.Links, links[:2])
	}
	snapshot = second.Snapshot()
	checkSessionNodes(t, snapshot,
		contextmap.SessionNode{Node: file, Activities: []contextmap.Activity{contextmap.ActivityTouched}},
	)
	if len(snapshot.Links) != 0 {
		t.Errorf("second session links = %+v, want none", snapshot.Links)
	}
}

func TestSessionLinksBetweenObservedFiles(t *testing.T) {
	start, end := sessionFile("index.md"), sessionFile("guide.md")
	link := contextmap.Link{Kind: "references", From: start.ID, To: end.ID}
	session := contextmap.NewSession(makeGraph(t, []contextmap.Node{start, end}, []contextmap.Link{link}))
	recordSession(t, session, contextmap.Observation{ID: start.ID, Activity: contextmap.ActivityRead})
	if links := session.Snapshot().Links; len(links) != 0 {
		t.Fatalf("links with one observed file = %+v, want none", links)
	}
	recordSession(t, session, contextmap.Observation{ID: end.ID, Activity: contextmap.ActivityConsidered})
	if links := session.Snapshot().Links; !reflect.DeepEqual(links, []contextmap.Link{link}) {
		t.Errorf("links with both files observed = %+v, want %+v", links, link)
	}
}

func TestSessionRejectsInvalidBatch(t *testing.T) {
	file := sessionFile("billing.md")
	billing := concept("billing")
	session := contextmap.NewSession(makeGraph(t, []contextmap.Node{file, billing}, nil))
	invalid := []contextmap.Observation{
		{ID: sessionFile("missing.md").ID, Activity: contextmap.ActivityRead},
		{ID: file.ID, Activity: "unknown"},
		{ID: file.ID, Activity: contextmap.ActivityIncluded},
		{ID: billing.ID, Activity: contextmap.ActivityRead},
	}
	for _, observation := range invalid {
		err := session.Record(contextmap.Observation{ID: file.ID, Activity: contextmap.ActivityRead}, observation)
		if err == nil {
			t.Errorf("accepted invalid observation %+v", observation)
		}
	}
	snapshot := session.Snapshot()
	if len(snapshot.Nodes) != 0 || len(snapshot.Links) != 0 {
		t.Errorf("invalid batch changed the session: %+v", snapshot)
	}
}

func TestSessionConcurrentRecordsAndSnapshots(t *testing.T) {
	file := sessionFile("guide.md")
	session := contextmap.NewSession(makeGraph(t, []contextmap.Node{file}, nil))
	t.Run("concurrent", func(t *testing.T) {
		for _, activity := range []contextmap.Activity{contextmap.ActivityRead, contextmap.ActivityTouched} {
			t.Run(string(activity), func(t *testing.T) {
				t.Parallel()
				recordSession(t, session, contextmap.Observation{ID: file.ID, Activity: activity})
				snapshot := session.Snapshot()
				if len(snapshot.Nodes) != 1 {
					t.Errorf("nodes = %+v, want the observed file", snapshot.Nodes)
				}
				view, found := session.DirectView(file.ID)
				if !found || !slices.Contains(view.Nodes[0].Activities, activity) {
					t.Errorf("focused view missing activity %q: %+v", activity, view)
				}
			})
		}
	})
	snapshot := session.Snapshot()
	if len(snapshot.Nodes) != 1 || len(snapshot.Nodes[0].Activities) != 2 {
		t.Errorf("nodes = %+v, want the file with both activities", snapshot.Nodes)
	}
}

func sessionFile(path string) contextmap.Node {
	return contextmap.Node{ID: contextmap.NodeID{Kind: contextmap.KindFile, Repository: "shop", Path: path}, Label: path}
}

func recordSession(t *testing.T, session *contextmap.Session, observations ...contextmap.Observation) {
	t.Helper()
	if err := session.Record(observations...); err != nil {
		t.Fatal(err)
	}
}

func checkSessionNodes(t *testing.T, snapshot contextmap.SessionSnapshot, want ...contextmap.SessionNode) {
	t.Helper()
	if !reflect.DeepEqual(snapshot.Nodes, want) {
		t.Errorf("nodes = %+v, want %+v", snapshot.Nodes, want)
	}
}
