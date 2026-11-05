package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-pipeprint/library/graph"
	"github.com/r59q/forge-tools/forge-pipeprint/library/render"
	pb "github.com/r59q/forge-tools/forge-pipeprint/service/pipeprintpb"
)

// Graph returns a pipeline's jobs and needs.
func (s *Server) Graph(ctx context.Context, r *pb.GraphRequest) (*pb.GraphResponse, error) {
	return call(s, ctx, func() (*pb.GraphResponse, error) {
		p, err := read(r)
		if err != nil {
			return nil, err
		}

		g := graph.Build(p)
		resp := &pb.GraphResponse{Name: g.Name, Levels: count(g.Levels), Triggers: render.Triggers(p.Triggers)}
		for _, n := range g.Nodes {
			resp.Nodes = append(resp.Nodes, node(n))
		}

		for _, e := range g.Edges {
			resp.Edges = append(resp.Edges, &pb.Edge{From: e.From, To: e.To})
		}

		return resp, nil
	})
}

func node(n graph.Node) *pb.Node {
	return &pb.Node{
		Id:        n.ID,
		Name:      n.Name,
		Stage:     n.Stage,
		Level:     count(n.Level),
		RunsOn:    n.RunsOn,
		Image:     n.Image,
		Steps:     n.Steps,
		Matrix:    n.Matrix,
		Condition: n.If,
	}
}

// Render draws a pipeline as text.
func (s *Server) Render(ctx context.Context, r *pb.RenderRequest) (*pb.RenderResponse, error) {
	return call(s, ctx, func() (*pb.RenderResponse, error) {
		if r.GetWidth() < 0 || r.GetWidth() > maxWidth {
			return nil, errBadRequest
		}

		p, err := read(r)
		if err != nil {
			return nil, err
		}

		text := render.Text(p, render.Options{Steps: r.GetSteps(), ASCII: r.GetAscii(), Width: int(r.GetWidth())})

		return &pb.RenderResponse{Text: text}, nil
	})
}
