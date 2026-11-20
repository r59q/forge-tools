package api

import (
	"net/http"
	"testing"

	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	pb "github.com/r59q/forge-tools/forge-agents/service/agentspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestTreeFiltersRESTAndGRPC(t *testing.T) {
	s := newServer(t, fakeExecutor{})
	client, server := dial(t, s), restServer(t, s)
	auth := "Bearer " + token
	ctx := authorized(t, auth)
	created, err := client.CreateAgent(ctx, &pb.CreateAgentRequest{Client: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	root := created.GetAgent()
	code, raw := do(t, server, http.MethodPost, "/v1/agents", &pb.CreateAgentRequest{Parent: root.GetId()}, auth)
	child := new(pb.Agent)
	if code != http.StatusOK || protojson.Unmarshal(raw, child) != nil {
		t.Fatalf("create child: %d %s", code, raw)
	}
	grand, err := client.CreateAgent(ctx, &pb.CreateAgentRequest{Parent: child.GetId()})
	if err != nil || grand.GetAgent().GetDepth() != 2 || grand.GetAgent().GetRoot() != root.GetId() {
		t.Fatalf("grandchild = %v, %v", grand, err)
	}
	if root.GetRoot() != root.GetId() || root.GetDepth() != 0 ||
		child.GetParent() != root.GetId() || child.GetRoot() != root.GetId() || child.GetDepth() != 1 {
		t.Fatalf("tree links: root %v, child %v", root, child)
	}
	depth := int32(0)
	list, err := client.ListAgents(ctx, &pb.ListAgentsRequest{Root: root.GetId(), Depth: &depth})
	if err != nil || len(list.GetAgents()) != 1 || list.GetAgents()[0].GetId() != root.GetId() {
		t.Fatalf("gRPC list = %v, %v", list, err)
	}
	path := "/v1/agents?root=" + root.GetId() + "&parent=" + root.GetId() + "&depth=1"
	code, raw = do(t, server, http.MethodGet, path, nil, auth)
	list = new(pb.ListAgentsResponse)
	if code != http.StatusOK || protojson.Unmarshal(raw, list) != nil ||
		len(list.GetAgents()) != 1 || list.GetAgents()[0].GetId() != child.GetId() {
		t.Fatalf("REST list: %d %s", code, raw)
	}
	events, err := client.ListEvents(ctx, &pb.ListEventsRequest{Id: root.GetId()})
	if err != nil || len(events.GetEvents()) != 1 ||
		events.GetEvents()[0].GetKind() != string(transcript.KindChildSpawned) || events.GetEvents()[0].GetChildId() != child.GetId() {
		t.Errorf("root events = %v, %v", events, err)
	}
	depth = -1
	_, err = client.ListAgents(ctx, &pb.ListAgentsRequest{Depth: &depth})
	if grpcCode(t, err) != codes.InvalidArgument {
		t.Errorf("negative depth: %v", err)
	}
}
