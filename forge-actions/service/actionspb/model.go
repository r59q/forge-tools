// Package actionspb contains the generated pipeline API and model conversions.
package actionspb

import (
	"time"

	"github.com/r59q/forge-tools/forge-actions/library/actions"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

// FromRef converts the library model to its protobuf representation.
func FromRef(v actions.Ref) *Ref {
	out := &Ref{}
	out.Source = v.Source
	out.Repository = v.Repository
	out.Id = v.ID

	return out
}

// ToRef converts a protobuf value to the library model. Nil is a zero value.
func ToRef(v *Ref) actions.Ref {
	if v == nil {
		return actions.Ref{}
	}

	out := actions.Ref{}
	out.Source = v.Source
	out.Repository = v.Repository
	out.ID = v.Id

	return out
}

// FromStatus converts the library model to its protobuf representation.
func FromStatus(v actions.Status) *Status {
	out := &Status{}
	out.Phase = v.Phase
	out.Result = v.Result
	out.NativeStatus = v.NativeStatus
	out.NativeResult = v.NativeResult

	return out
}

// ToStatus converts a protobuf value to the library model. Nil is a zero value.
func ToStatus(v *Status) actions.Status {
	if v == nil {
		return actions.Status{}
	}

	out := actions.Status{}
	out.Phase = v.Phase
	out.Result = v.Result
	out.NativeStatus = v.NativeStatus
	out.NativeResult = v.NativeResult

	return out
}

// FromPipeline converts the library model to its protobuf representation.
func FromPipeline(v actions.Pipeline) *Pipeline {
	out := &Pipeline{}
	out.Ref = FromRef(v.Ref)
	out.Name = v.Name
	out.Commit = v.Commit
	out.Branch = v.Branch
	out.DefinitionId = v.DefinitionID
	out.Event = v.Event
	out.WebUrl = v.WebURL
	out.Status = FromStatus(v.Status)
	out.Attempt = v.Attempt
	out.CreatedAt = timestamp(v.CreatedAt)
	out.StartedAt = timestamp(v.StartedAt)
	out.FinishedAt = timestamp(v.FinishedAt)
	out.UpdatedAt = timestamp(v.UpdatedAt)

	return out
}

// ToPipeline converts a protobuf value to the library model. Nil is a zero value.
func ToPipeline(v *Pipeline) actions.Pipeline {
	if v == nil {
		return actions.Pipeline{}
	}

	out := actions.Pipeline{}
	out.Ref = ToRef(v.Ref)
	out.Name = v.Name
	out.Commit = v.Commit
	out.Branch = v.Branch
	out.DefinitionID = v.DefinitionId
	out.Event = v.Event
	out.WebURL = v.WebUrl
	out.Status = ToStatus(v.Status)
	out.Attempt = v.Attempt
	if v.CreatedAt != nil {
		out.CreatedAt = v.CreatedAt.AsTime()
	}
	if v.StartedAt != nil {
		out.StartedAt = v.StartedAt.AsTime()
	}
	if v.FinishedAt != nil {
		out.FinishedAt = v.FinishedAt.AsTime()
	}
	if v.UpdatedAt != nil {
		out.UpdatedAt = v.UpdatedAt.AsTime()
	}

	return out
}

// FromStep converts the library model to its protobuf representation.
func FromStep(v actions.Step) *Step {
	out := &Step{}
	out.Number = v.Number
	out.Name = v.Name
	out.Status = FromStatus(v.Status)
	out.StartedAt = timestamp(v.StartedAt)
	out.FinishedAt = timestamp(v.FinishedAt)

	return out
}

// ToStep converts a protobuf value to the library model. Nil is a zero value.
func ToStep(v *Step) actions.Step {
	if v == nil {
		return actions.Step{}
	}

	out := actions.Step{}
	out.Number = v.Number
	out.Name = v.Name
	out.Status = ToStatus(v.Status)
	if v.StartedAt != nil {
		out.StartedAt = v.StartedAt.AsTime()
	}
	if v.FinishedAt != nil {
		out.FinishedAt = v.FinishedAt.AsTime()
	}

	return out
}

// FromJob converts the library model to its protobuf representation.
func FromJob(v actions.Job) *Job {
	out := &Job{}
	out.Ref = FromRef(v.Ref)
	out.PipelineId = v.PipelineID
	out.Name = v.Name
	out.Stage = v.Stage
	out.WebUrl = v.WebURL
	out.Runner = v.Runner
	out.Status = FromStatus(v.Status)
	out.Attempt = v.Attempt
	out.AllowFailure = v.AllowFailure
	out.StartedAt = timestamp(v.StartedAt)
	out.FinishedAt = timestamp(v.FinishedAt)
	out.StepsKnown = v.StepsKnown
	for _, x := range v.Steps {
		out.Steps = append(out.Steps, FromStep(x))
	}

	return out
}

// ToJob converts a protobuf value to the library model. Nil is a zero value.
func ToJob(v *Job) actions.Job {
	if v == nil {
		return actions.Job{}
	}

	out := actions.Job{}
	out.Ref = ToRef(v.Ref)
	out.PipelineID = v.PipelineId
	out.Name = v.Name
	out.Stage = v.Stage
	out.WebURL = v.WebUrl
	out.Runner = v.Runner
	out.Status = ToStatus(v.Status)
	out.Attempt = v.Attempt
	out.AllowFailure = v.AllowFailure
	if v.StartedAt != nil {
		out.StartedAt = v.StartedAt.AsTime()
	}
	if v.FinishedAt != nil {
		out.FinishedAt = v.FinishedAt.AsTime()
	}

	out.StepsKnown = v.StepsKnown
	for _, x := range v.Steps {
		out.Steps = append(out.Steps, ToStep(x))
	}

	return out
}

// FromArtifact converts the library model to its protobuf representation.
func FromArtifact(v actions.Artifact) *Artifact {
	out := &Artifact{}
	out.Ref = FromRef(v.Ref)
	out.PipelineId = v.PipelineID
	out.JobId = v.JobID
	out.Name = v.Name
	out.Size = v.Size
	out.Availability = v.Availability
	out.CreatedAt = timestamp(v.CreatedAt)
	out.ExpiresAt = timestamp(v.ExpiresAt)

	return out
}

// ToArtifact converts a protobuf value to the library model. Nil is a zero value.
func ToArtifact(v *Artifact) actions.Artifact {
	if v == nil {
		return actions.Artifact{}
	}

	out := actions.Artifact{}
	out.Ref = ToRef(v.Ref)
	out.PipelineID = v.PipelineId
	out.JobID = v.JobId
	out.Name = v.Name
	out.Size = v.Size
	out.Availability = v.Availability
	if v.CreatedAt != nil {
		out.CreatedAt = v.CreatedAt.AsTime()
	}
	if v.ExpiresAt != nil {
		out.ExpiresAt = v.ExpiresAt.AsTime()
	}

	return out
}

// FromDefinition converts the library model to its protobuf representation.
func FromDefinition(v actions.Definition) *Definition {
	out := &Definition{}
	out.Id = v.ID
	out.Name = v.Name
	out.Path = v.Path
	out.WebUrl = v.WebURL
	out.State = v.State

	return out
}

// ToDefinition converts a protobuf value to the library model. Nil is a zero value.
func ToDefinition(v *Definition) actions.Definition {
	if v == nil {
		return actions.Definition{}
	}

	out := actions.Definition{}
	out.ID = v.Id
	out.Name = v.Name
	out.Path = v.Path
	out.WebURL = v.WebUrl
	out.State = v.State

	return out
}

// FromLog converts the library model to its protobuf representation.
func FromLog(v actions.Log) *Log {
	out := &Log{}
	out.Text = v.Text
	out.Truncated = v.Truncated

	return out
}

// ToLog converts a protobuf value to the library model. Nil is a zero value.
func ToLog(v *Log) actions.Log {
	if v == nil {
		return actions.Log{}
	}

	out := actions.Log{}
	out.Text = v.Text
	out.Truncated = v.Truncated

	return out
}
