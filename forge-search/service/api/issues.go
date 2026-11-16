package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-search/library/issue"
	"github.com/r59q/forge-tools/forge-search/library/search"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// KindIssueRepository is the kind of the document that names the
// repository of an index IndexIssues made: its ID is the repository as
// JSON. It has no content, and searches never match it.
const KindIssueRepository search.Kind = "issue-repository"

// issuesPrefix starts the name of every index IndexIssues makes.
// IndexCheckout refuses such names, so that code and issues never share an
// index.
const issuesPrefix = "issues-"

// errNoIssues means no issue reader is configured.
var errNoIssues = errors.New("api: no issues service configured")

// issueSearchLimits returns the bounds of both issue searches. Their result
// limit is the same, so that a state filter cuts both at the same match.
func issueSearchLimits() issue.Limits {
	return issue.Limits{Results: MaxResults}
}

// IndexIssues reads a repository's issues and syncs them into its issue
// index, creating it first. Calls for one repository run one at a time;
// others fail with ABORTED before they read anything.
func (s *Server) IndexIssues(ctx context.Context, r *pb.IndexIssuesRequest) (*pb.IndexIssuesResponse, error) {
	return call(s, ctx, s.config.IndexTimeout, func(ctx context.Context, engine *search.Engine) (*pb.IndexIssuesResponse, error) {
		repo, err := issueRepository(r.GetRepository())
		if err != nil {
			return nil, err
		}
		if err := s.checkIssues(); err != nil {
			return nil, err
		}
		index := issueIndex(repo)

		release, err := s.reserve(index)
		if err != nil {
			return nil, err
		}
		defer release()

		if err := s.createIssueIndex(ctx, index, repo); err != nil {
			return nil, err
		}
		result, err := issue.Sync(ctx, s.config.Issues, engine, index, repo, issue.Limits{})
		if err != nil {
			return nil, err
		}

		status, err := s.status(ctx, index)
		if err != nil {
			return nil, err
		}
		return &pb.IndexIssuesResponse{
			Index:     status,
			Added:     count(result.Added),
			Replaced:  count(result.Replaced),
			Removed:   count(result.Removed),
			Unchanged: count(result.Unchanged),
			TooLarge:  count(result.Issues.TooLarge),
			SyncedAt:  timestamppb.New(result.SyncedAt),
		}, nil
	})
}

// SearchIndexedIssues searches the issue index of a repository.
func (s *Server) SearchIndexedIssues(
	ctx context.Context, r *pb.SearchIndexedIssuesRequest,
) (*pb.SearchIndexedIssuesResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, engine *search.Engine) (*pb.SearchIndexedIssuesResponse, error) {
		repo, err := issueRepository(r.GetRepository())
		if err != nil {
			return nil, err
		}
		query, options, err := issueQuery(r.GetQuery(), repo)
		if err != nil {
			return nil, err
		}

		status, err := s.status(ctx, issueIndex(repo))
		if err != nil {
			return nil, err
		}
		results, err := issue.SearchIndex(ctx, engine, status.GetName(), query, options)
		if err != nil {
			return nil, err
		}
		if err := canceled(ctx, results.State); err != nil {
			return nil, err
		}

		return &pb.SearchIndexedIssuesResponse{
			Matches: issueMatches(results.Matches),
			State:   searchState(results.State),
			Index:   status,
		}, nil
	})
}

// SearchUnindexedIssues reads and searches a repository's issues, keeping
// nothing.
func (s *Server) SearchUnindexedIssues(
	ctx context.Context, r *pb.SearchUnindexedIssuesRequest,
) (*pb.SearchUnindexedIssuesResponse, error) {
	return call(s, ctx, s.config.IndexTimeout, func(ctx context.Context, _ *search.Engine) (*pb.SearchUnindexedIssuesResponse, error) {
		repo, err := issueRepository(r.GetRepository())
		if err != nil {
			return nil, err
		}
		query, options, err := issueQuery(r.GetQuery(), repo)
		if err != nil {
			return nil, err
		}
		if err := s.checkIssues(); err != nil {
			return nil, err
		}

		results, err := issue.SearchRepository(ctx, s.config.Issues, repo, query, options)
		if err != nil {
			return nil, err
		}
		if err := canceled(ctx, results.State); err != nil {
			return nil, err
		}

		return &pb.SearchUnindexedIssuesResponse{
			Matches:  issueMatches(results.Matches),
			State:    searchState(results.State),
			TooLarge: count(results.Issues.TooLarge),
		}, nil
	})
}

// checkIssues returns errNoIssues unless an issue reader is configured.
func (s *Server) checkIssues() error {
	if s.config.Issues == nil {
		return errNoIssues
	}
	return nil
}

// createIssueIndex creates the issue index of a repository unless it
// exists, and records the repository in it.
func (s *Server) createIssueIndex(ctx context.Context, index string, repo issues.RepositoryRef) error {
	engine := s.config.Engine

	_, err := engine.Index(ctx, index)
	if errors.Is(err, search.ErrNotFound) {
		err = s.create(ctx, index)
	}
	if err != nil {
		return err
	}

	ref := search.Ref{Kind: KindIssueRepository, ID: issueRecord(repo), Version: "1"}
	return engine.Put(ctx, index, search.Document{Ref: ref, Content: []byte{}})
}

// issueStatus adds the repository of an issue index, and the time of its
// last completed sync, to its status.
func (s *Server) issueStatus(ctx context.Context, status *pb.Index, record string) (*pb.Index, error) {
	repo, err := parseIssueRecord(record)
	if err != nil {
		return nil, fmt.Errorf("index issue repository: %w", err)
	}
	status.Issues = &pb.IssueRepository{Source: repo.Source, Repository: repo.Repository}

	synced, err := issue.LastSynced(ctx, s.config.Engine, status.GetName(), repo)
	if err != nil {
		return nil, err
	}
	if !synced.IsZero() {
		status.UpdatedAt = timestamppb.New(synced)
	}
	return status, nil
}

// issueRepository validates a requested repository.
func issueRepository(r *pb.IssueRepository) (issues.RepositoryRef, error) {
	if !validSource(r.GetSource()) || !validRepository(r.GetRepository()) {
		return issues.RepositoryRef{}, errBadRequest
	}
	return issues.RepositoryRef{Source: r.GetSource(), Repository: r.GetRepository()}, nil
}

// issueIndex is the name of a repository's issue index: a hash of its
// fields, so that no field reaches the name.
func issueIndex(repo issues.RepositoryRef) string {
	sum := sha256.Sum256([]byte(repo.Source + "\x00" + repo.Repository))
	return issuesPrefix + hex.EncodeToString(sum[:20])
}

// issueTarget is the JSON form of an issue index's repository.
type issueTarget struct {
	Source     string `json:"source"`
	Repository string `json:"repository"`
}

func issueRecord(repo issues.RepositoryRef) string {
	b, err := json.Marshal(issueTarget(repo))
	if err != nil {
		panic(err) // A struct of strings always encodes.
	}
	return string(b)
}

func parseIssueRecord(record string) (issues.RepositoryRef, error) {
	var t issueTarget
	if err := json.Unmarshal([]byte(record), &t); err != nil {
		return issues.RepositoryRef{}, err
	}
	return issues.RepositoryRef(t), nil
}

// issueQuery returns the library query and options of an IssueQuery on a
// repository. The library validates the text.
func issueQuery(q *pb.IssueQuery, repo issues.RepositoryRef) (search.Query, issue.Options, error) {
	limit, err := resultLimit(q.GetMaxResults())
	if err != nil {
		return search.Query{}, issue.Options{}, err
	}

	state := issues.State(q.GetState())
	if state != "" && state != issues.Open && state != issues.Closed {
		return search.Query{}, issue.Options{}, errBadRequest
	}

	options := issue.Options{
		Filter: issue.Filter{Source: repo.Source, Repository: repo.Repository, State: state},
		Limits: issueSearchLimits(),
	}
	return search.Query{Text: q.GetText(), Limit: limit}, options, nil
}

func issueMatches(found []issue.Match) []*pb.IssueMatch {
	out := make([]*pb.IssueMatch, len(found))
	for i, m := range found {
		out[i] = &pb.IssueMatch{
			Number:        m.Issue.Number,
			Title:         m.Issue.Title,
			State:         string(m.Issue.State),
			WebUrl:        m.Issue.WebURL,
			Field:         string(m.Field),
			Range:         rangeOf(m.Range),
			Preview:       m.Preview,
			PreviewColumn: count(m.PreviewColumn),
		}
	}
	return out
}
