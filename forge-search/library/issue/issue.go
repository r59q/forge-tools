// Package issue turns forge issues into issue documents for the search
// package: it searches issues unindexed, and syncs a repository's issues into
// an index so that later syncs only touch the issues that changed.
//
// An issue is one document, its title on the first line and its body below,
// so a search sees each issue entirely old or entirely new. Matches name the
// field they are in, with a range relative to that field. Comments are not
// indexed.
//
// Issue text is untrusted. Bodies, issue counts and results are bounded, and
// errors never contain issue text.
package issue

import (
	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-search/library/search"
)

// MaxTitleBytes is the longest title. Issues with longer titles are left out
// and counted in State.TooLarge.
const MaxTitleBytes = 1024

// MaxURLBytes is the longest web URL. Issues with longer URLs are left out
// and counted in State.TooLarge.
const MaxURLBytes = 2048

// KindSync is the kind of the document that records when a repository was
// last synced. Issue searches never see it.
const KindSync search.Kind = "issue-sync"

// Field is the part of an issue a match is in.
type Field string

// Searched fields.
const (
	// FieldTitle is the issue title, always one line.
	FieldTitle Field = "title"

	// FieldBody is the issue description.
	FieldBody Field = "body"
)

// Limits bound one search or sync. Zero fields use the default:
// DefaultLimits for searches, DefaultSyncLimits for Sync.
type Limits struct {
	// BodyBytes is the largest body. Issues with larger bodies are left out
	// and counted in State.TooLarge.
	BodyBytes int `json:"bodyBytes"`

	// Issues is the most issues one unindexed search or sync reads. More
	// return search.ErrTooLarge.
	Issues int `json:"issues"`

	// Results is the most matches one search returns. More set
	// search.State.Truncated.
	Results int `json:"results"`
}

// DefaultLimits returns the bounds of one search: 256 KiB per body, 10000
// issues and 1000 results.
func DefaultLimits() Limits {
	return Limits{BodyBytes: 256 << 10, Issues: 10000, Results: 1000}
}

// DefaultSyncLimits returns the bounds of one sync: 256 KiB per body and
// 50000 issues, which fit the default index limits of the search package.
func DefaultSyncLimits() Limits {
	return Limits{BodyBytes: 256 << 10, Issues: 50000, Results: 1000}
}

func (l Limits) withDefaults(defaults Limits) Limits {
	if l.BodyBytes <= 0 {
		l.BodyBytes = defaults.BodyBytes
	}
	if l.Issues <= 0 {
		l.Issues = defaults.Issues
	}
	if l.Results <= 0 {
		l.Results = defaults.Results
	}
	return l
}

// Filter selects issues. Empty fields select everything.
type Filter struct {
	// Source selects the issues of one issues.Source ID.
	Source string `json:"source,omitempty"`

	// Repository selects the issues of one repository path, such as
	// "owner/name".
	Repository string `json:"repository,omitempty"`

	// State selects issues.Open or issues.Closed issues.
	State issues.State `json:"state,omitempty"`
}

// Options configure a search.
type Options struct {
	Filter Filter `json:"filter"`

	// Limits bound the work. Zero fields use DefaultLimits.
	Limits Limits `json:"limits"`
}

// Info is what a match tells about its issue. Source, Repository and Number
// identify it.
type Info struct {
	Source     string       `json:"source,omitempty"`
	Repository string       `json:"repository"`
	Number     int64        `json:"number"`
	Title      string       `json:"title"`
	State      issues.State `json:"state"`
	WebURL     string       `json:"webUrl,omitempty"`
}

// Match is one occurrence of the query text in an issue.
type Match struct {
	Issue Info  `json:"issue"`
	Field Field `json:"field"`

	// Range covers the matched text, relative to Field, with the rules of
	// search.Range.
	Range search.Range `json:"range"`

	// Preview and PreviewColumn follow the rules of search.Match.
	Preview       string `json:"preview"`
	PreviewColumn int    `json:"previewColumn"`
}

// State counts the issues a search or sync left out.
type State struct {
	// TooLarge counts issues over Limits.BodyBytes, MaxTitleBytes or
	// MaxURLBytes.
	TooLarge int `json:"tooLarge"`
}

// Results hold the matches in match order and the state of the search.
type Results struct {
	Matches []Match      `json:"matches"`
	State   search.State `json:"state"`

	// Issues counts the issues an unindexed search left out. Indexed
	// searches leave it zero; Sync reports them instead.
	Issues State `json:"issues"`
}
