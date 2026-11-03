package api

import (
	"context"
	"encoding/json"

	"github.com/r59q/forge-tools/forge-review/library/review"
	pb "github.com/r59q/forge-tools/forge-review/service/reviewpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// encode fills out from v through the documented JSON field mapping shared by
// domain and protobuf output. It returns err when set, rather than converting
// partial data, and propagates conversion failures.
func encode(v any, out proto.Message, err error) error {
	if err != nil {
		return err
	}

	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	return protojson.Unmarshal(b, out)
}

// encodeReview fills a response whose only field is a review.
func encodeReview(v review.Review, out proto.Message, err error) error {
	return encode(map[string]any{"review": v}, out, err)
}

func anchor(a *pb.Anchor) *review.Anchor {
	if a == nil {
		return nil
	}

	return &review.Anchor{
		Path:  a.Path,
		Side:  a.Side,
		Start: int(a.Start),
		End:   int(a.End),
	}
}

func native(v review.NativeResult, err error) (*pb.NativeResult, error) {
	if err != nil {
		return nil, err
	}

	return &pb.NativeResult{
		Storage:   v.Storage,
		SubjectId: v.SubjectID,
		Resource:  v.Resource,
		DataJson:  string(v.Data),
	}, nil
}

// CreateSubject captures a fixed snapshot through the library.
func (s *Server) CreateSubject(ctx context.Context, r *pb.CreateSubjectRequest) (*pb.CreateSubjectResponse, error) {
	return call(s, ctx, r.GetKind() == "comparison" ||
		r.GetKind() == "change_request", func(ctx context.Context, c *review.Client) (*pb.CreateSubjectResponse, error) {
		in := review.Input{
			Kind:  r.GetKind(),
			Patch: r.GetPatch(),
			From:  r.GetFrom(),
			To:    r.GetTo(),
		}
		if r.GetRef() != nil {
			in.Ref = review.Ref{Source: r.Ref.Source, Repository: r.Ref.Repository, Number: r.Ref.Number}
		}

		for _, f := range r.GetContext() {
			in.Context = append(in.Context, review.FileContext{Path: f.GetPath(), Side: f.GetSide(), Content: f.GetContent()})
		}

		v, err := c.CreateSubject(ctx, in)
		out := &pb.CreateSubjectResponse{}

		return out, encode(map[string]any{"subject": v}, out, err)
	})
}

// GetSubject returns stored diff and context without contacting a forge.
func (s *Server) GetSubject(ctx context.Context, r *pb.GetSubjectRequest) (*pb.GetSubjectResponse, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.GetSubjectResponse, error) {
		v, e := c.GetSubject(ctx, r.GetSubjectId())
		out := &pb.GetSubjectResponse{}

		return out, encode(map[string]any{"subject": v}, out, e)
	})
}

// RequestReview creates a standalone reviewer assignment.
func (s *Server) RequestReview(ctx context.Context, r *pb.RequestReviewRequest) (*pb.RequestReviewResponse, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.RequestReviewResponse, error) {
		v, e := c.RequestReview(ctx, r.GetSubjectId(), r.GetReviewer())
		out := &pb.RequestReviewResponse{}

		return out, encodeReview(v, out, e)
	})
}

// GetReview reads a standalone review and its threads.
func (s *Server) GetReview(ctx context.Context, r *pb.GetReviewRequest) (*pb.GetReviewResponse, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.GetReviewResponse, error) {
		v, e := c.GetReview(ctx, r.GetReviewId())
		out := &pb.GetReviewResponse{}

		return out, encodeReview(v, out, e)
	})
}

// ListReviews lists matching standalone reviews.
func (s *Server) ListReviews(ctx context.Context, r *pb.ListReviewsRequest) (*pb.ListReviewsResponse, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.ListReviewsResponse, error) {
		v, e := c.ListReviews(ctx, r.GetSubjectId(), r.GetReviewer(), r.GetStatus())
		out := &pb.ListReviewsResponse{}

		return out, encode(map[string]any{"reviews": v}, out, e)
	})
}

// AddComment opens a standalone thread at a validated location.
func (s *Server) AddComment(ctx context.Context, r *pb.AddCommentRequest) (*pb.AddCommentResponse, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.AddCommentResponse, error) {
		v, e := c.AddComment(ctx, r.GetReviewId(), r.GetAuthor(), r.GetBody(), anchor(r.GetAnchor()))
		out := &pb.AddCommentResponse{}

		return out, encodeReview(v, out, e)
	})
}

// Reply appends a standalone reply.
func (s *Server) Reply(ctx context.Context, r *pb.ReplyRequest) (*pb.ReplyResponse, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.ReplyResponse, error) {
		v, e := c.Reply(ctx, r.GetReviewId(), r.GetThreadId(), r.GetAuthor(), r.GetBody())
		out := &pb.ReplyResponse{}

		return out, encodeReview(v, out, e)
	})
}

// Resolve changes standalone thread resolution.
func (s *Server) Resolve(ctx context.Context, r *pb.ResolveRequest) (*pb.ResolveResponse, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.ResolveResponse, error) {
		v, e := c.Resolve(ctx, r.GetReviewId(), r.GetThreadId(), r.GetResolved())
		out := &pb.ResolveResponse{}

		return out, encodeReview(v, out, e)
	})
}

// Submit records a standalone assessment.
func (s *Server) Submit(ctx context.Context, r *pb.SubmitRequest) (*pb.SubmitResponse, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.SubmitResponse, error) {
		v, e := c.Submit(ctx, r.GetReviewId(), r.GetStatus(), r.GetBody())
		out := &pb.SubmitResponse{}

		return out, encodeReview(v, out, e)
	})
}

// GetCapabilities reports adapter support for a connector source.
func (s *Server) GetCapabilities(ctx context.Context, r *pb.GetCapabilitiesRequest) (*pb.GetCapabilitiesResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.GetCapabilitiesResponse, error) {
		v, e := c.Capabilities(r.GetSource())
		out := &pb.GetCapabilitiesResponse{}

		return out, encode(map[string]any{"capabilities": v}, out, e)
	})
}

// RequestNativeReview publishes native reviewer assignments.
func (s *Server) RequestNativeReview(ctx context.Context, r *pb.RequestNativeReviewRequest) (*pb.RequestNativeReviewResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.RequestNativeReviewResponse, error) {
		result, err := native(c.RequestNativeReview(ctx, r.GetSubjectId(), r.GetReviewers()))
		return &pb.RequestNativeReviewResponse{Result: result}, err
	})
}

// ListNative reads native reviews, discussions or assignments.
func (s *Server) ListNative(ctx context.Context, r *pb.ListNativeRequest) (*pb.ListNativeResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.ListNativeResponse, error) {
		result, err := native(c.ListNative(ctx, r.GetSubjectId(), r.GetResource(), r.GetReviewId()))
		return &pb.ListNativeResponse{Result: result}, err
	})
}

// SubmitNative publishes a native assessment.
func (s *Server) SubmitNative(ctx context.Context, r *pb.SubmitNativeRequest) (*pb.SubmitNativeResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.SubmitNativeResponse, error) {
		result, err := native(c.SubmitNative(ctx, r.GetSubjectId(), r.GetStatus(), r.GetBody()))
		return &pb.SubmitNativeResponse{Result: result}, err
	})
}

// CommentNative publishes native general, file or line feedback.
func (s *Server) CommentNative(ctx context.Context, r *pb.CommentNativeRequest) (*pb.CommentNativeResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.CommentNativeResponse, error) {
		result, err := native(c.CommentNative(ctx, r.GetSubjectId(), r.GetBody(), anchor(r.GetAnchor())))
		return &pb.CommentNativeResponse{Result: result}, err
	})
}

// ReplyNative publishes a reply to a native thread.
func (s *Server) ReplyNative(ctx context.Context, r *pb.ReplyNativeRequest) (*pb.ReplyNativeResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.ReplyNativeResponse, error) {
		result, err := native(c.ReplyNative(ctx, r.GetSubjectId(), r.GetThreadId(), r.GetBody()))
		return &pb.ReplyNativeResponse{Result: result}, err
	})
}

// ResolveNative changes native thread resolution where supported.
func (s *Server) ResolveNative(ctx context.Context, r *pb.ResolveNativeRequest) (*pb.ResolveNativeResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.ResolveNativeResponse, error) {
		result, err := native(c.ResolveNative(ctx, r.GetSubjectId(), r.GetThreadId(), r.GetResolved()))
		return &pb.ResolveNativeResponse{Result: result}, err
	})
}
