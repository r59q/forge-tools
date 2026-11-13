package api

import (
	"context"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-symbols/library/navigation"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
	pb "github.com/r59q/forge-tools/forge-symbols/service/symbolspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// callee defines Greet, which caller calls from Main.
const callee = `package sample

// Greet returns a greeting for name.
func Greet(name string) string {
	return format(name)
}

func format(name string) string {
	return "hello " + name
}
`

const caller = `package sample

func Main() {
	println(Greet("world"))
}
`

func sample() []*pb.File {
	return []*pb.File{
		{Path: "callee.go", Content: []byte(callee)},
		{Path: "caller.go", Content: []byte(caller)},
	}
}

// mainResult is Main in caller.go, at index 0.
func mainResult() *pb.DefinitionResult {
	return &pb.DefinitionResult{
		Path:  "caller.go",
		Index: proto.Int32(0),
		Definition: &pb.Definition{
			Name:      "Main",
			Kind:      "function",
			NameRange: &pb.Range{StartLine: 3, StartColumn: 6, EndLine: 3, EndColumn: 10},
			Range:     &pb.Range{StartLine: 3, StartColumn: 1, EndLine: 5, EndColumn: 2},
		},
		Tier:      pb.Tier_TIER_FILE,
		Precision: "heuristic",
	}
}

// callResult is the reference to name at index in caller.go, inside Main.
func callResult(name string, index int32, column int32) *pb.ReferenceResult {
	return &pb.ReferenceResult{
		Path:  "caller.go",
		Index: index,
		Reference: &pb.Reference{
			Name:      name,
			Kind:      "call",
			Range:     &pb.Range{StartLine: 4, StartColumn: column, EndLine: 4, EndColumn: column + int32(len(name))},
			Enclosing: proto.Int32(0),
		},
		Precision: "heuristic",
	}
}

func equal(t *testing.T, name string, got, want proto.Message) {
	t.Helper()
	if !proto.Equal(got, want) {
		t.Errorf("%s:\n got %v\nwant %v", name, got, want)
	}
}

func TestAnalyze(t *testing.T) {
	for _, tr := range transports(t, server(t), "Bearer "+token) {
		resp := &pb.AnalyzeResponse{}
		if code := tr.call(t, "Analyze", &pb.AnalyzeRequest{Files: sample()}, resp); code != codes.OK {
			t.Fatalf("%s: %v", tr.name, code)
		}

		analyses := resp.GetAnalyses()
		if len(analyses) != 2 || analyses[0].GetPath() != "callee.go" || analyses[1].GetPath() != "caller.go" {
			t.Fatalf("%s: analyses %v", tr.name, analyses)
		}

		greet := analyses[0].GetDefinitions()[0]
		equal(t, tr.name+" Greet", greet, &pb.Definition{
			Name:      "Greet",
			Kind:      "function",
			NameRange: &pb.Range{StartLine: 4, StartColumn: 6, EndLine: 4, EndColumn: 11},
			Range:     &pb.Range{StartLine: 4, StartColumn: 1, EndLine: 6, EndColumn: 2},
		})

		got := analyses[1]
		if got.GetLanguage() != "go" || got.GetBytes() != int32(len(caller)) || len(got.GetDefinitions()) != 1 {
			t.Errorf("%s: caller.go %v", tr.name, got)
		}

		equal(t, tr.name+" Greet call", got.GetReferences()[1], callResult("Greet", 1, 10).GetReference())
	}
}

func TestOutgoing(t *testing.T) {
	onMain := &pb.Target{Path: "caller.go", Line: 3, Column: 6}
	for _, tr := range transports(t, server(t), "Bearer "+token) {
		resp := &pb.OutgoingResponse{}
		if code := tr.call(t, "Outgoing", &pb.OutgoingRequest{Files: sample(), Target: onMain}, resp); code != codes.OK {
			t.Fatalf("%s: %v", tr.name, code)
		}

		equal(t, tr.name, resp, &pb.OutgoingResponse{
			Name: "Main",
			Groups: []*pb.OutgoingGroup{{
				Definition: mainResult(),
				References: []*pb.ReferenceResult{callResult("println", 0, 2), callResult("Greet", 1, 10)},
			}},
		})

		resp = &pb.OutgoingResponse{}
		if code := tr.call(t, "Outgoing", &pb.OutgoingRequest{Files: sample(), Target: onMain, MaxResults: 2}, resp); code != codes.OK ||
			!resp.GetTruncated() || len(resp.GetGroups()[0].GetReferences()) != 1 {
			t.Errorf("%s truncated: %v %v", tr.name, code, resp)
		}
	}
}

func TestIncoming(t *testing.T) {
	for _, tr := range transports(t, server(t), "Bearer "+token) {
		resp := &pb.IncomingResponse{}
		req := &pb.IncomingRequest{Files: sample(), Target: &pb.Target{Name: "Greet", Path: "callee.go"}}
		if code := tr.call(t, "Incoming", req, resp); code != codes.OK {
			t.Fatalf("%s: %v", tr.name, code)
		}

		mainCaller := mainResult()
		mainCaller.Tier = pb.Tier_TIER_OTHER
		equal(t, tr.name, resp, &pb.IncomingResponse{
			Name:   "Greet",
			Groups: []*pb.IncomingGroup{{Caller: mainCaller, References: []*pb.ReferenceResult{callResult("Greet", 1, 10)}}},
		})
	}
}

func TestDefinitions(t *testing.T) {
	onCall := &pb.Target{Path: "caller.go", Line: 4, Column: 11}
	for _, tr := range transports(t, server(t), "Bearer "+token) {
		resp := &pb.DefinitionsResponse{}
		if code := tr.call(t, "Definitions", &pb.DefinitionsRequest{Files: sample(), Target: onCall}, resp); code != codes.OK {
			t.Fatalf("%s: %v", tr.name, code)
		}

		if resp.GetName() != "Greet" || resp.GetTruncated() || len(resp.GetDefinitions()) != 1 {
			t.Fatalf("%s: %v", tr.name, resp)
		}

		got := resp.GetDefinitions()[0]
		if got.GetPath() != "callee.go" || got.Index == nil || got.GetIndex() != 0 || got.GetDefinition().GetName() != "Greet" ||
			got.GetTier() != pb.Tier_TIER_OTHER || got.GetPrecision() != string(navigation.Heuristic) {
			t.Errorf("%s: %v", tr.name, got)
		}
	}
}

// TestRESTJSON checks that zero values are explicit and unset optional
// fields are absent.
func TestRESTJSON(t *testing.T) {
	handler, err := server(t).REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	body, err := protojson.Marshal(&pb.OutgoingRequest{Files: sample(), Target: &pb.Target{Path: "caller.go", Line: 3, Column: 6}})
	if err != nil {
		t.Fatal(err)
	}

	rec := rest(handler, "/v1/symbols/outgoing", body, int64(len(body)), "Bearer "+token)
	got := rec.Body.String()
	for _, want := range []string{`"index":0`, `"nested":false`, `"truncated":false`, `"tier":"TIER_FILE"`, `"precision":"heuristic"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}

	if strings.Contains(got, `"parent"`) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("unset parent or caching: %s %v", got, rec.Header())
	}
}

func TestConvert(t *testing.T) {
	fileLevel := definitionResult(navigation.DefinitionResult{Path: "a.go", Index: symbols.None, Tier: navigation.TierFile})
	if fileLevel.Index != nil || fileLevel.Definition != nil || fileLevel.GetTier() != pb.Tier_TIER_FILE {
		t.Errorf("file level: %v", fileLevel)
	}

	if got := definition(symbols.Definition{Parent: 2}); got.Parent == nil || got.GetParent() != 2 {
		t.Errorf("parent: %v", got)
	}

	if got := tier(navigation.Tier(9)); got != pb.Tier_TIER_UNSPECIFIED {
		t.Errorf("unknown tier: %v", got)
	}

	for n, want := range map[int]int32{-1: 0, 7: 7, 1 << 40: 1<<31 - 1} {
		if got := count(n); got != want {
			t.Errorf("count(%d) = %d", n, got)
		}
	}
}
