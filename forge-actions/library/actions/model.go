// Package actions reads pipelines, jobs, logs and artifacts across forges.
// Callers supply authenticated sessions from forge-connector.
package actions

import (
	"errors"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// ErrInvalid means a request or source is malformed.
var ErrInvalid = errors.New("actions: invalid argument")

// ErrUnknownSource means a source ID is not configured.
var ErrUnknownSource = errors.New("actions: unknown source")

// ErrUnsupported means the adapter or connection cannot supply a feature.
var ErrUnsupported = errors.New("actions: unsupported operation")

// ErrTooLarge means a listing or artifact exceeds the documented limit.
var ErrTooLarge = errors.New("actions: limit exceeded")

// Source is a connection supplied by forge-connector.
type Source struct {
	// ID is the connection ID.
	ID string
	// Session supplies authenticated forge API requests.
	Session *forgeauth.Session
}

// Capabilities describes implemented adapter features, subject to forge
// version, permissions and repository configuration. Downloads may require
// the connector's external download host configuration.
type Capabilities struct {
	// Definitions reports support for listing configured workflows.
	Definitions bool
	// Steps reports support for native step details.
	Steps bool
	// Stages reports support for native stage names.
	Stages bool
	// JobLogs reports support for reading job log snapshots.
	JobLogs bool
	// Artifacts reports support for archive listing and downloads.
	Artifacts bool
}

// SourceInfo identifies a source without exposing its session.
type SourceInfo struct {
	// ID is the connection ID.
	ID string
	// Kind identifies the forge implementation.
	Kind forgeauth.Kind
	// Capabilities describes the implemented adapter features.
	Capabilities Capabilities
}

// RepositoryRef identifies a repository on one required source.
type RepositoryRef struct {
	// Source is the required connection ID.
	Source string
	// Repository is an unencoded full repository path.
	Repository string
}

// Ref identifies a pipeline, job or artifact by its native ID.
// IDs are decimal strings; GitLab artifact IDs are the producing job IDs.
type Ref struct {
	// RepositoryRef identifies the source and repository.
	RepositoryRef
	// ID is the native resource ID.
	ID string
}

// Status separates lifecycle from outcome and preserves native values.
// Phase is unknown, queued, running, waiting or finished. Result is empty
// until known, then success, failure, cancelled, skipped, neutral or unknown.
type Status struct {
	// Phase describes lifecycle progress.
	Phase string
	// Result describes the outcome, when known.
	Result string
	// NativeStatus is the original forge status.
	NativeStatus string
	// NativeResult is the original separate conclusion, if present.
	NativeResult string
}

// Pipeline is one execution of automation. Zero times and empty fields are
// unavailable. Commit is the tested commit, which may differ from a proposal's
// head. Ref retains the forge's ref spelling.
type Pipeline struct {
	// Ref identifies this execution or output on its repository.
	Ref
	// Name is the display name reported by the forge.
	Name string
	// Commit is the tested commit ID.
	Commit string
	// Branch retains the native branch or ref spelling.
	Branch string
	// DefinitionID identifies the native workflow, when available.
	DefinitionID string
	// Event is the native trigger or pipeline source.
	Event string
	// WebURL opens the resource on its forge.
	WebURL string
	// Status describes progress and outcome.
	Status Status
	// Attempt is the native attempt number; zero means unavailable.
	Attempt int64
	// CreatedAt is the native creation time, or zero.
	CreatedAt time.Time
	// StartedAt is the native start time, or zero.
	StartedAt time.Time
	// FinishedAt is the native completion time, or zero.
	FinishedAt time.Time
	// UpdatedAt is the native last-update time, or zero.
	UpdatedAt time.Time
}

// Job is a unit of work in a pipeline. StepsKnown distinguishes unavailable
// step detail from an empty step list. Stage is optional. AllowFailure says
// whether the forge allows this job to fail, when exposed.
type Job struct {
	// Ref identifies this execution or output on its repository.
	Ref
	// PipelineID identifies the containing execution.
	PipelineID string
	// Name is the display name reported by the forge.
	Name string
	// Stage is the native job grouping, when available.
	Stage string
	// WebURL opens the resource on its forge.
	WebURL string
	// Runner is the native runner name or description, when available.
	Runner string
	// Status describes progress and outcome.
	Status Status
	// Attempt is the native attempt number; zero means unavailable.
	Attempt int64
	// AllowFailure marks a native job whose failure is permitted.
	AllowFailure bool
	// StartedAt is the native start time, or zero.
	StartedAt time.Time
	// FinishedAt is the native completion time, or zero.
	FinishedAt time.Time
	// StepsKnown distinguishes unavailable step data from an empty list.
	StepsKnown bool
	// Steps contains native steps when StepsKnown is true.
	Steps []Step
}

// Step is an operation within a job; Number is the native step number.
type Step struct {
	// Number is the native step number.
	Number int64
	// Name is the display name reported by the forge.
	Name string
	// Status describes progress and outcome.
	Status Status
	// StartedAt is the native start time, or zero.
	StartedAt time.Time
	// FinishedAt is the native completion time, or zero.
	FinishedAt time.Time
}

// Definition describes a configured automation process, where exposed.
type Definition struct {
	// ID is the native resource ID.
	ID string
	// Name is the display name reported by the forge.
	Name string
	// Path locates the configuration file within its repository.
	Path string
	// WebURL opens the resource on its forge.
	WebURL string
	// State is the native definition state, such as active or disabled.
	State string
}

// Artifact describes a downloadable archive. Size is -1 when unknown.
// Availability is available, expired or unknown; permissions are checked
// when downloading. JobID is set when the forge associates it with a job.
type Artifact struct {
	// Ref identifies this execution or output on its repository.
	Ref
	// PipelineID identifies the containing execution.
	PipelineID string
	// JobID identifies the producing job, when available.
	JobID string
	// Name is the display name reported by the forge.
	Name string
	// Size is the archive size in bytes, or -1 when unknown.
	Size int64
	// Availability is available, expired or unknown.
	Availability string
	// CreatedAt is the native creation time, or zero.
	CreatedAt time.Time
	// ExpiresAt is the native archive expiry time, or zero.
	ExpiresAt time.Time
}

// ListOptions selects one page of pipelines. Page defaults to 1; pages contain
// at most 50 native entries. Phase and Result filter those entries locally,
// so an empty page can still have a NextPage. Branch is a branch name;
// DefinitionID is the native definition ID (GitHub) or workflow filename
// (Forgejo). GitLab does not support the definition filter.
type ListOptions struct {
	// RepositoryRef identifies the source and repository.
	RepositoryRef
	// Page is the native page number; zero means the first page.
	Page int
	// Commit is the tested commit ID.
	Commit string
	// Branch retains the native branch or ref spelling.
	Branch string
	// DefinitionID identifies the native workflow, when available.
	DefinitionID string
	// Phase describes lifecycle progress.
	Phase string
	// Result describes the outcome, when known.
	Result string
}

// PipelinePage is a page of pipelines. NextPage is zero at the end. Listings
// are live and can move between requests; they are not snapshots.
type PipelinePage struct {
	// Pipelines contains this page’s matching executions.
	Pipelines []Pipeline
	// NextPage is the next page number, or zero at the end.
	NextPage int
}

// Log is a UTF-8 job log snapshot, limited to 1 MiB. Truncated means more
// bytes were available. Logs can change while a job is running.
type Log struct {
	// Text contains UTF-8 log content.
	Text string
	// Truncated reports that bytes were omitted after the size limit.
	Truncated bool
}

// MaxArtifactBytes is the largest archive returned by DownloadArtifact.
const MaxArtifactBytes = 16 << 20
