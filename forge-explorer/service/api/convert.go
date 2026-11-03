package api

import (
	"math"

	"github.com/r59q/forge-tools/forge-explorer/library/explorer"
	pb "github.com/r59q/forge-tools/forge-explorer/service/explorerpb"
)

// Domain values are bounded by the library to well within int32.
func source(s explorer.Source) *pb.Source {
	return &pb.Source{Id: s.ID, Label: s.Label, Version: s.Version, Kind: s.Kind,
		Capabilities: &pb.Capabilities{Symbols: s.Capabilities.Symbols, Definitions: s.Capabilities.Definitions,
			References: s.Capabilities.References, History: s.Capabilities.History},
		Limits: &pb.Limits{FileBytes: number(s.Limits.FileBytes), Entries: number(s.Limits.Entries),
			Results: number(s.Limits.Results), SearchBytes: number(s.Limits.SearchBytes), Depth: number(s.Limits.Depth)},
	}
}

func state(s explorer.State) *pb.State {
	return &pb.State{Truncated: s.Truncated, Canceled: s.Canceled, Skipped: number(s.Skipped)}
}

func location(l explorer.Location) *pb.Location {
	return &pb.Location{Source: l.Source, Version: l.Version, Path: l.Path, Range: &pb.Range{
		StartLine: number(l.Range.StartLine), StartColumn: number(l.Range.StartColumn),
		EndLine: number(l.Range.EndLine), EndColumn: number(l.Range.EndColumn),
	}}
}

// number checks the library budget invariant before conversion.
func number(n int) int32 {
	if n < 0 || n > math.MaxInt32 {
		panic("explorer library exceeded protobuf numeric bounds")
	}
	return int32(n)
}
