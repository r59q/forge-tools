package api

import (
	"context"
	"encoding/json"

	"github.com/r59q/forge-tools/forge-review/library/review"
	pb "github.com/r59q/forge-tools/forge-review/service/reviewpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// encode shares the documented JSON field mapping between domain and protobuf
// output. Conversion failures propagate, rather than returning partial data.
func encode[T proto.Message](v any, out T, err error) (T, error) {
	if err != nil {
		return out, err
	}
	b, err := json.Marshal(v)
	if err == nil {
		err = protojson.Unmarshal(b, out)
	}
	return out, err
}
func anchor(a *pb.Anchor) *review.Anchor {
	if a == nil {
		return nil
	}
	return &review.Anchor{Path: a.Path, Side: a.Side, Start: int(a.Start), End: int(a.End)}
}
func native(v review.NativeResult, err error) (*pb.NativeResponse, error) {
	if err != nil {
		return nil, err
	}
	return &pb.NativeResponse{Storage: v.Storage, SubjectId: v.SubjectID, Resource: v.Resource, DataJson: string(v.Data)}, nil
}

// CreateSubject captures a fixed snapshot through the library.
func (s *Server) CreateSubject(ctx context.Context, r *pb.CreateSubjectRequest) (*pb.Subject, error) {
	return call(s, ctx, r.GetKind() == "comparison" || r.GetKind() == "change_request", func(ctx context.Context, c *review.Client) (*pb.Subject, error) {
		in := review.Input{Kind: r.GetKind(), Patch: r.GetPatch(), From: r.GetFrom(), To: r.GetTo()}
		if r.GetRef() != nil {
			in.Ref = review.Ref{Source: r.Ref.Source, Repository: r.Ref.Repository, Number: r.Ref.Number}
		}
		for _, f := range r.GetContext() {
			in.Context = append(in.Context, review.FileContext{Path: f.GetPath(), Side: f.GetSide(), Content: f.GetContent()})
		}
		v, err := c.CreateSubject(ctx, in)
		return encode(v, &pb.Subject{}, err)
	})
}

// GetSubject returns stored diff and context without contacting a forge.
func (s *Server) GetSubject(ctx context.Context, r *pb.SubjectRequest) (*pb.Subject, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.Subject, error) {
		v, e := c.GetSubject(ctx, r.GetSubjectId())
		return encode(v, &pb.Subject{}, e)
	})
}

// RequestReview creates a standalone reviewer assignment.
func (s *Server) RequestReview(ctx context.Context, r *pb.RequestReviewRequest) (*pb.Review, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.Review, error) {
		v, e := c.RequestReview(ctx, r.GetSubjectId(), r.GetReviewer())
		return encode(v, &pb.Review{}, e)
	})
}

// GetReview reads a standalone review and its threads.
func (s *Server) GetReview(ctx context.Context, r *pb.ReviewRequest) (*pb.Review, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.Review, error) {
		v, e := c.GetReview(ctx, r.GetReviewId())
		return encode(v, &pb.Review{}, e)
	})
}

// ListReviews lists matching standalone reviews.
func (s *Server) ListReviews(ctx context.Context, r *pb.ListReviewsRequest) (*pb.ListReviewsResponse, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.ListReviewsResponse, error) {
		v, e := c.ListReviews(ctx, r.GetSubjectId(), r.GetReviewer(), r.GetStatus())
		return encode(map[string]any{"reviews": v}, &pb.ListReviewsResponse{}, e)
	})
}

// AddComment opens a standalone thread at a validated location.
func (s *Server) AddComment(ctx context.Context, r *pb.CommentRequest) (*pb.Review, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.Review, error) {
		v, e := c.AddComment(ctx, r.GetReviewId(), r.GetAuthor(), r.GetBody(), anchor(r.GetAnchor()))
		return encode(v, &pb.Review{}, e)
	})
}

// Reply appends a standalone reply.
func (s *Server) Reply(ctx context.Context, r *pb.ReplyRequest) (*pb.Review, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.Review, error) {
		v, e := c.Reply(ctx, r.GetReviewId(), r.GetThreadId(), r.GetAuthor(), r.GetBody())
		return encode(v, &pb.Review{}, e)
	})
}

// Resolve changes standalone thread resolution.
func (s *Server) Resolve(ctx context.Context, r *pb.ResolveRequest) (*pb.Review, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.Review, error) {
		v, e := c.Resolve(ctx, r.GetReviewId(), r.GetThreadId(), r.GetResolved())
		return encode(v, &pb.Review{}, e)
	})
}

// Submit records a standalone assessment.
func (s *Server) Submit(ctx context.Context, r *pb.SubmitRequest) (*pb.Review, error) {
	return call(s, ctx, false, func(ctx context.Context, c *review.Client) (*pb.Review, error) {
		v, e := c.Submit(ctx, r.GetReviewId(), r.GetStatus(), r.GetBody())
		return encode(v, &pb.Review{}, e)
	})
}

// GetCapabilities reports adapter support for a connector source.
func (s *Server) GetCapabilities(ctx context.Context, r *pb.SourceRequest) (*pb.Capabilities, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.Capabilities, error) {
		v, e := c.Capabilities(r.GetSource())
		return encode(v, &pb.Capabilities{}, e)
	})
}

// RequestNativeReview publishes native reviewer assignments.
func (s *Server) RequestNativeReview(ctx context.Context, r *pb.NativeReviewersRequest) (*pb.NativeResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.NativeResponse, error) {
		return native(c.RequestNativeReview(ctx, r.GetSubjectId(), r.GetReviewers()))
	})
}

// ListNative reads native reviews, discussions or assignments.
func (s *Server) ListNative(ctx context.Context, r *pb.NativeListRequest) (*pb.NativeResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.NativeResponse, error) {
		return native(c.ListNative(ctx, r.GetSubjectId(), r.GetResource(), r.GetReviewId()))
	})
}

// SubmitNative publishes a native assessment.
func (s *Server) SubmitNative(ctx context.Context, r *pb.NativeSubmitRequest) (*pb.NativeResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.NativeResponse, error) {
		return native(c.SubmitNative(ctx, r.GetSubjectId(), r.GetStatus(), r.GetBody()))
	})
}

// CommentNative publishes native general, file or line feedback.
func (s *Server) CommentNative(ctx context.Context, r *pb.NativeCommentRequest) (*pb.NativeResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.NativeResponse, error) {
		return native(c.CommentNative(ctx, r.GetSubjectId(), r.GetBody(), anchor(r.GetAnchor())))
	})
}

// ReplyNative publishes a reply to a native thread.
func (s *Server) ReplyNative(ctx context.Context, r *pb.NativeReplyRequest) (*pb.NativeResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.NativeResponse, error) {
		return native(c.ReplyNative(ctx, r.GetSubjectId(), r.GetThreadId(), r.GetBody()))
	})
}

// ResolveNative changes native thread resolution where supported.
func (s *Server) ResolveNative(ctx context.Context, r *pb.NativeResolveRequest) (*pb.NativeResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, c *review.Client) (*pb.NativeResponse, error) {
		return native(c.ResolveNative(ctx, r.GetSubjectId(), r.GetThreadId(), r.GetResolved()))
	})
}
