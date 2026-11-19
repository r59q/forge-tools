package store

import (
	"math"

	pb "github.com/r59q/forge-tools/forge-chat/service/chatpb"
)

// ValidID reports whether an ID can be safely used as a chat key.
func ValidID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, ch := range id {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') &&
			(ch < '0' || ch > '9') && ch != '-' && ch != '_' {
			return false
		}
	}
	return true
}

func validPatch(req *pb.UpdateChatMetadataRequest) bool {
	if req == nil || !ValidID(req.GetId()) {
		return false
	}
	if req.Archived == nil && req.Links == nil && req.IslandPosition == nil {
		return false
	}
	ids := req.GetLinks().GetIds()
	if len(ids) > 100 {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !ValidID(id) || id == req.GetId() || seen[id] {
			return false
		}
		seen[id] = true
	}
	position := req.GetIslandPosition()
	for _, v := range []float64{position.GetX(), position.GetY(), position.GetZ()} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}
