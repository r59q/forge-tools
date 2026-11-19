package store_test

import (
	"errors"
	"math"
	"testing"

	pb "github.com/r59q/forge-tools/forge-chat/service/chatpb"
	"github.com/r59q/forge-tools/forge-chat/service/store"
	"google.golang.org/protobuf/proto"
)

func TestPreferencesSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	data, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	original := &pb.UpdateChatMetadataRequest{
		Id: "chat", Archived: proto.Bool(true),
		Links:          &pb.ChatLinks{Ids: []string{"other"}},
		IslandPosition: &pb.IslandPosition{X: 3, Y: 2, Z: 1},
	}
	if _, err := data.Patch(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}

	data, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = data.Close() }()

	updated, err := data.Patch(t.Context(), &pb.UpdateChatMetadataRequest{Id: "chat", Archived: proto.Bool(false)})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetArchived() || !proto.Equal(updated.GetLinks(), original.GetLinks()) ||
		!proto.Equal(updated.GetIslandPosition(), original.GetIslandPosition()) {
		t.Fatalf("archive update lost preferences: %v", updated)
	}
	updated, err = data.Patch(t.Context(), &pb.UpdateChatMetadataRequest{Id: "chat", Links: &pb.ChatLinks{}})
	if err != nil || len(updated.GetLinks().GetIds()) != 0 {
		t.Fatalf("clear links: %v, %v", updated, err)
	}
	got, err := data.List(t.Context())
	if err != nil || len(got) != 1 || !proto.Equal(updated, got[0]) {
		t.Fatalf("list after restart = %v, %v", got, err)
	}
}

func TestPatchRejectsInvalidMetadata(t *testing.T) {
	data, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = data.Close() }()

	for _, request := range []*pb.UpdateChatMetadataRequest{
		{Id: "../chat", Archived: proto.Bool(true)},
		{Id: "chat"},
		{Id: "chat", Links: &pb.ChatLinks{Ids: []string{"other", "other"}}},
		{Id: "chat", Links: &pb.ChatLinks{Ids: []string{"chat"}}},
		{Id: "chat", IslandPosition: &pb.IslandPosition{Y: math.Inf(1)}},
	} {
		if _, err := data.Patch(t.Context(), request); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("Patch(%v) = %v", request, err)
		}
	}
}
