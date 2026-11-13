package api

import (
	"math"

	"github.com/r59q/forge-tools/forge-symbols/library/navigation"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
	pb "github.com/r59q/forge-tools/forge-symbols/service/symbolspb"
)

func files(in []*pb.File) []symbols.File {
	out := make([]symbols.File, len(in))
	for i, file := range in {
		out[i] = symbols.File{Path: file.GetPath(), Content: file.GetContent(), Language: file.GetLanguage()}
	}

	return out
}

func target(in *pb.Target) navigation.Target {
	return navigation.Target{
		Name:   in.GetName(),
		Kind:   in.GetKind(),
		Path:   in.GetPath(),
		Line:   int(in.GetLine()),
		Column: int(in.GetColumn()),
	}
}

func analysis(in symbols.Analysis) *pb.Analysis {
	return &pb.Analysis{
		Path:        in.Path,
		Language:    in.Language,
		Bytes:       count(in.Bytes),
		Definitions: convert(in.Definitions, definition),
		References:  convert(in.References, reference),
	}
}

func definition(in symbols.Definition) *pb.Definition {
	return &pb.Definition{
		Name:      in.Name,
		Kind:      in.Kind,
		NameRange: span(in.NameRange),
		Range:     span(in.Range),
		Container: in.Container,
		Parent:    index(in.Parent),
	}
}

func reference(in symbols.Reference) *pb.Reference {
	return &pb.Reference{
		Name:      in.Name,
		Kind:      in.Kind,
		Range:     span(in.Range),
		Enclosing: index(in.Enclosing),
	}
}

func span(in symbols.Range) *pb.Range {
	return &pb.Range{
		StartLine:   count(in.StartLine),
		StartColumn: count(in.StartColumn),
		EndLine:     count(in.EndLine),
		EndColumn:   count(in.EndColumn),
	}
}

// definitionResult leaves index and definition unset for the file level.
func definitionResult(in navigation.DefinitionResult) *pb.DefinitionResult {
	out := &pb.DefinitionResult{
		Path:      in.Path,
		Index:     index(in.Index),
		Tier:      tier(in.Tier),
		Precision: string(in.Precision),
	}
	if in.Index != symbols.None {
		out.Definition = definition(in.Definition)
	}

	return out
}

func referenceResult(in navigation.ReferenceResult) *pb.ReferenceResult {
	return &pb.ReferenceResult{
		Path:      in.Path,
		Index:     count(in.Index),
		Reference: reference(in.Reference),
		Nested:    in.Nested,
		Precision: string(in.Precision),
	}
}

func tier(in navigation.Tier) pb.Tier {
	switch in {
	case navigation.TierFile:
		return pb.Tier_TIER_FILE
	case navigation.TierContainer:
		return pb.Tier_TIER_CONTAINER
	case navigation.TierOther:
		return pb.Tier_TIER_OTHER
	}

	return pb.Tier_TIER_UNSPECIFIED
}

func outgoing(in navigation.OutgoingResult) *pb.OutgoingResponse {
	groups := make([]*pb.OutgoingGroup, len(in.Groups))
	for i, group := range in.Groups {
		groups[i] = &pb.OutgoingGroup{
			Definition: definitionResult(group.Definition),
			References: convert(group.References, referenceResult),
		}
	}

	return &pb.OutgoingResponse{Name: in.Name, Groups: groups, Truncated: in.Truncated}
}

func incoming(in navigation.IncomingResult) *pb.IncomingResponse {
	groups := make([]*pb.IncomingGroup, len(in.Groups))
	for i, group := range in.Groups {
		groups[i] = &pb.IncomingGroup{
			Caller:     definitionResult(group.Caller),
			References: convert(group.References, referenceResult),
		}
	}

	return &pb.IncomingResponse{Name: in.Name, Groups: groups, Truncated: in.Truncated}
}

func definitions(in navigation.DefinitionsResult) *pb.DefinitionsResponse {
	return &pb.DefinitionsResponse{
		Name:        in.Name,
		Definitions: convert(in.Definitions, definitionResult),
		Truncated:   in.Truncated,
	}
}

func convert[T, R any](in []T, fn func(T) R) []R {
	out := make([]R, len(in))
	for i, item := range in {
		out[i] = fn(item)
	}

	return out
}

// index returns nil for symbols.None, and n otherwise.
func index(n int) *int32 {
	if n == symbols.None {
		return nil
	}

	i := count(n)

	return &i
}

// count converts a line, column, index or length, clamped to
// [0, math.MaxInt32].
func count(n int) int32 {
	return int32(max(0, min(n, math.MaxInt32)))
}
