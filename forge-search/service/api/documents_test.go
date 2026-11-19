package api

import (
	"bytes"
	"testing"

	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func TestDocumentLifecycle(t *testing.T) {
	f := newFixture(t, nil)
	for _, tr := range transports(t, f.s, "Bearer "+token) {
		t.Run(tr.name, func(t *testing.T) {
			index := "texts"
			files := []*pb.Document{
				{Path: "b", Content: []byte("find me")},
				{Path: "a", Content: []byte("😀find me")},
				{Path: "binary", Content: []byte{0xff}},
			}
			live := &pb.SearchDocumentsRequest{Documents: files, Text: "fi"}
			query := &pb.SearchDocumentsRequest{Index: index, Text: live.Text}
			original := must[*pb.SearchDocumentsResponse](t, tr, live)
			if len(original.Matches) != 2 || original.Matches[0].Path != "a" ||
				original.Matches[0].Range.StartColumn != 3 || original.Matches[0].Range.EndColumn != 5 || original.State.Skipped != 1 {
				t.Fatalf("unindexed: %v", original)
			}
			if indexes := must[*pb.ListIndexesResponse](t, tr, &pb.ListIndexesRequest{}); len(indexes.Indexes) != 0 {
				t.Fatalf("unindexed search created %v", indexes)
			}

			create := &pb.CreateIndexRequest{Index: index}
			must[*pb.CreateIndexResponse](t, tr, create)
			if got := tr.call(t, create, response(create)); got != codes.AlreadyExists {
				t.Fatalf("duplicate create: %v", got)
			}
			for _, file := range files {
				must[*pb.PutDocumentResponse](t, tr, &pb.PutDocumentRequest{Index: index, Document: file})
			}
			equal(t, "initial matches", must[*pb.SearchDocumentsResponse](t, tr, query), original)
			query.MaxResults, live.MaxResults = 1, 1
			limited := must[*pb.SearchDocumentsResponse](t, tr, query)
			equal(t, "limit", limited, must[*pb.SearchDocumentsResponse](t, tr, live))
			if !limited.State.Truncated || len(limited.Matches) != 1 {
				t.Fatalf("expected one match and truncation: %v", limited)
			}
			query.MaxResults, live.MaxResults = 0, 0

			files[0].Content = []byte("changed")
			must[*pb.PutDocumentResponse](t, tr, &pb.PutDocumentRequest{Index: index, Document: files[0]})
			must[*pb.RemoveDocumentResponse](t, tr, &pb.RemoveDocumentRequest{Index: index, Path: "a"})
			live.Documents = []*pb.Document{files[0], files[2]}
			after := must[*pb.SearchDocumentsResponse](t, tr, query)
			equal(t, "after edits", after, must[*pb.SearchDocumentsResponse](t, tr, live))
			if len(after.Matches) != 0 {
				t.Fatalf("stale matches: %v", after)
			}

			must[*pb.DeleteIndexResponse](t, tr, &pb.DeleteIndexRequest{Index: index})
			if got := tr.call(t, query, response(query)); got != codes.NotFound {
				t.Fatalf("deleted index: %v", got)
			}
		})
	}
}

func TestDocumentLimits(t *testing.T) {
	f := newFixture(t, nil)
	full := &pb.Document{Path: "sample", Content: bytes.Repeat([]byte("a"), 1<<20)}
	oversized := &pb.Document{Path: full.Path, Content: append(bytes.Clone(full.Content), 'a')}
	for _, tr := range transports(t, f.s, "Bearer "+token) {
		req := &pb.SearchDocumentsRequest{Documents: []*pb.Document{full}, Text: "x"}
		must[*pb.SearchDocumentsResponse](t, tr, req)
		for _, tc := range []struct {
			req  proto.Message
			code codes.Code
		}{
			{&pb.SearchDocumentsRequest{Index: "main", Documents: req.Documents, Text: "x"}, codes.InvalidArgument},
			{&pb.SearchDocumentsRequest{Documents: []*pb.Document{full, full}, Text: "x"}, codes.InvalidArgument},
			{&pb.SearchDocumentsRequest{Documents: []*pb.Document{full, full, full, full, full}, Text: "x"}, codes.ResourceExhausted},
			{&pb.SearchDocumentsRequest{Documents: make([]*pb.Document, maxDocuments+1), Text: "x"}, codes.ResourceExhausted},
			{&pb.SearchDocumentsRequest{Documents: []*pb.Document{oversized}, Text: "x"}, codes.ResourceExhausted},
			{&pb.PutDocumentRequest{Index: "main", Document: oversized}, codes.ResourceExhausted},
			{&pb.PutDocumentRequest{Index: "repo-reserved", Document: full}, codes.InvalidArgument},
			{&pb.CreateIndexRequest{Index: "../escape"}, codes.InvalidArgument},
			{&pb.SearchDocumentsRequest{Text: "x", MaxResults: MaxResults + 1}, codes.InvalidArgument},
		} {
			if got := tr.call(t, tc.req, response(tc.req)); got != tc.code {
				t.Errorf("%s %s: %v, want %v", tr.name, method(tc.req), got, tc.code)
			}
		}
	}
}
