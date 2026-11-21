package contextmap_test

import (
	"slices"
	"testing"

	"github.com/r59q/forge-tools/forge-context/library/contextmap"
)

func TestSessionViewsFollowLinksAndPreserveActivity(t *testing.T) {
	guide, example := sessionFile("guide.md"), sessionFile("example.md")
	billing := concept("billing")
	links := []contextmap.Link{
		{Kind: contextmap.LinkSupportedBy, From: billing.ID, To: guide.ID},
		{Kind: contextmap.LinkSupportedBy, From: billing.ID, To: example.ID},
	}
	unrelated := sessionFile("other.md")
	session := contextmap.NewSession(makeGraph(t, []contextmap.Node{guide, example, billing, unrelated}, links))
	recordSession(t, session,
		contextmap.Observation{ID: guide.ID, Activity: contextmap.ActivityRead},
		contextmap.Observation{ID: example.ID, Activity: contextmap.ActivityConsidered},
		contextmap.Observation{ID: unrelated.ID, Activity: contextmap.ActivityTouched},
	)

	direct, found := session.DirectView(guide.ID)
	if !found || direct.Focus != guide.ID || !slices.Equal(direct.Links, links[:1]) {
		t.Fatalf("unexpected direct view: %+v", direct)
	}
	read := contextmap.SessionNode{Node: guide, Activities: []contextmap.Activity{contextmap.ActivityRead}}
	checkSessionNodes(t, direct.SessionSnapshot, read, contextmap.SessionNode{Node: billing})
	direct.Nodes[0].Activities[0] = contextmap.ActivityTouched
	direct.Links[0].To = unrelated.ID

	expanded, found := session.ExpandedView(guide.ID)
	if !found || expanded.Focus != guide.ID || !slices.Equal(expanded.Links, links) {
		t.Fatalf("unexpected expanded view: %+v", expanded)
	}
	checkSessionNodes(t, expanded.SessionSnapshot, read, contextmap.SessionNode{Node: billing},
		contextmap.SessionNode{Node: example, Activities: []contextmap.Activity{contextmap.ActivityConsidered}},
	)
	if len(session.Snapshot().Links) != 0 {
		t.Fatal("exploring the graph recorded activity on the connecting concept")
	}
}

func TestSessionViewsFromUnobservedAndMissingFocus(t *testing.T) {
	file := sessionFile("guide.md")
	session := contextmap.NewSession(makeGraph(t, []contextmap.Node{file}, nil))
	for _, query := range []func(contextmap.NodeID) (contextmap.SessionView, bool){session.DirectView, session.ExpandedView} {
		view, found := query(file.ID)
		if !found || view.Focus != file.ID || view.Links == nil || len(view.Links) != 0 {
			t.Errorf("unexpected isolated view: %+v", view)
		}
		checkSessionNodes(t, view.SessionSnapshot, contextmap.SessionNode{Node: file})
		if _, found := query(concept("missing").ID); found {
			t.Error("found a missing focus")
		}
	}

	var empty contextmap.Session
	if _, found := empty.ExpandedView(file.ID); found {
		t.Error("found a focus in an empty session")
	}
}
