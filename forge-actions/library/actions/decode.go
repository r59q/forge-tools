package actions

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Phases a Status can be in.
const (
	phaseUnknown  = "unknown"
	phaseQueued   = "queued"
	phaseRunning  = "running"
	phaseWaiting  = "waiting"
	phaseFinished = "finished"
)

func normalize(kind forgeauth.Kind, state, result string) Status {
	s := Status{Phase: phaseUnknown, NativeStatus: state, NativeResult: result}
	if kind == forgeauth.GitHub {
		switch state {
		case "queued", "requested", "pending":
			s.Phase = phaseQueued
		case "in_progress":
			s.Phase = phaseRunning
		case "waiting":
			s.Phase = phaseWaiting
		case "completed":
			s.Phase = phaseFinished
		}

		if result != "" {
			s.Result = outcome(result)
		}

		return s
	}

	switch state {
	case "created", "pending", "preparing", "waiting_for_resource":
		s.Phase = phaseQueued
	case "running", "canceling":
		s.Phase = phaseRunning
	case "manual", "scheduled", "blocked":
		s.Phase = phaseWaiting
	case "waiting":
		s.Phase = phaseQueued
	case "success", "failure", "failed", "cancelled", "canceled", "skipped":
		s.Phase = phaseFinished
		s.Result = outcome(state)
	}

	return s
}

func outcome(v string) string {
	switch v {
	case "success", "skipped", "neutral":
		return v
	case "failure", "failed", "timed_out", "startup_failure", "action_required", "stale":
		return "failure"
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return "unknown"
	}
}

func nativeID(n int64) string {
	if n <= 0 {
		return ""
	}

	return strconv.FormatInt(n, 10)
}

func decodePipeline(kind forgeauth.Kind, raw json.RawMessage) (Pipeline, error) {
	var v struct {
		ID           int64
		Name         string
		Title        string
		Ref          string
		Status       string
		Conclusion   string
		Event        string
		Source       string
		DisplayTitle string          `json:"display_title"`
		HeadSHA      string          `json:"head_sha"`
		SHA          string          `json:"sha"`
		CommitSHA    string          `json:"commit_sha"`
		HeadBranch   string          `json:"head_branch"`
		PrettyRef    string          `json:"prettyref"`
		WorkflowID   json.RawMessage `json:"workflow_id"`
		HTMLURL      string          `json:"html_url"`
		WebURL       string          `json:"web_url"`
		Attempt      int64           `json:"run_attempt"`
		CreatedAt    time.Time       `json:"created_at"`
		UpdatedAt    time.Time       `json:"updated_at"`
		RunStartedAt time.Time       `json:"run_started_at"`
		StartedAt    time.Time       `json:"started_at"`
		FinishedAt   time.Time       `json:"finished_at"`
		Created      time.Time
		Updated      time.Time
		Started      time.Time
		Stopped      time.Time
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return Pipeline{}, err
	}
	if v.ID <= 0 {
		return Pipeline{}, fmt.Errorf("actions: missing pipeline ID")
	}

	p := Pipeline{
		Ref:       Ref{ID: nativeID(v.ID)},
		Name:      v.DisplayTitle,
		Commit:    v.HeadSHA,
		Branch:    v.HeadBranch,
		Event:     v.Event,
		WebURL:    v.HTMLURL,
		Status:    normalize(kind, v.Status, v.Conclusion),
		Attempt:   v.Attempt,
		CreatedAt: v.CreatedAt,
		UpdatedAt: v.UpdatedAt,
		StartedAt: v.RunStartedAt,
	}
	if p.Name == "" {
		p.Name = v.Name
	}

	var err error
	p.DefinitionID, err = decodeDefinitionID(v.WorkflowID)
	if err != nil {
		return Pipeline{}, err
	}

	switch kind {
	case forgeauth.GitLab:
		p.Commit, p.Branch, p.Event, p.WebURL = v.SHA, v.Ref, v.Source, v.WebURL
		p.StartedAt, p.FinishedAt = v.StartedAt, v.FinishedAt
	case forgeauth.Forgejo:
		p.Name, p.Commit, p.Branch = v.Title, v.CommitSHA, v.PrettyRef
		p.CreatedAt, p.UpdatedAt, p.StartedAt, p.FinishedAt = v.Created, v.Updated, v.Started, v.Stopped
	}

	return p, nil
}

func decodeJob(kind forgeauth.Kind, raw json.RawMessage) (Job, error) {
	var v struct {
		ID           int64
		Name         string
		Stage        string
		Status       string
		Conclusion   string
		RunID        int64 `json:"run_id"`
		RunAttempt   int64 `json:"run_attempt"`
		Attempt      int64
		HTMLURL      string `json:"html_url"`
		WebURL       string `json:"web_url"`
		RunnerName   string `json:"runner_name"`
		Runner       *struct{ Description string }
		Pipeline     struct{ ID int64 }
		AllowFailure bool      `json:"allow_failure"`
		StartedAt    time.Time `json:"started_at"`
		CompletedAt  time.Time `json:"completed_at"`
		FinishedAt   time.Time `json:"finished_at"`
		Steps        json.RawMessage
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return Job{}, err
	}
	if v.ID <= 0 {
		return Job{}, fmt.Errorf("actions: missing job ID")
	}

	j := Job{
		Ref:          Ref{ID: nativeID(v.ID)},
		PipelineID:   nativeID(v.RunID),
		Name:         v.Name,
		Stage:        v.Stage,
		WebURL:       v.HTMLURL,
		Status:       normalize(kind, v.Status, v.Conclusion),
		Attempt:      v.RunAttempt,
		Runner:       v.RunnerName,
		StartedAt:    v.StartedAt,
		FinishedAt:   v.CompletedAt,
		AllowFailure: v.AllowFailure,
	}
	if kind == forgeauth.GitLab {
		j.PipelineID, j.WebURL, j.FinishedAt = nativeID(v.Pipeline.ID), v.WebURL, v.FinishedAt
		if v.Runner != nil {
			j.Runner = v.Runner.Description
		}
	}

	if kind == forgeauth.Forgejo {
		j.Attempt = v.Attempt
	}
	if len(v.Steps) > 0 && string(v.Steps) != "null" && kind != forgeauth.GitLab {
		steps, err := decodeSteps(kind, v.Steps)
		if err != nil {
			return Job{}, err
		}

		j.StepsKnown = true
		j.Steps = steps
	}

	return j, nil
}

func decodeDefinitionID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw[0] == '"' {
		var id string
		err := json.Unmarshal(raw, &id)

		return id, err
	}

	var id int64
	if err := json.Unmarshal(raw, &id); err != nil {
		return "", err
	}

	return nativeID(id), nil
}

func decodeSteps(kind forgeauth.Kind, raw json.RawMessage) ([]Step, error) {
	var native []struct {
		Number      int64
		Name        string
		Status      string
		Conclusion  string
		StartedAt   time.Time `json:"started_at"`
		CompletedAt time.Time `json:"completed_at"`
		Started     time.Time
		Stopped     time.Time
	}
	if err := json.Unmarshal(raw, &native); err != nil {
		return nil, err
	}

	var steps []Step
	for _, v := range native {
		step := Step{
			Number:     v.Number,
			Name:       v.Name,
			Status:     normalize(kind, v.Status, v.Conclusion),
			StartedAt:  v.StartedAt,
			FinishedAt: v.CompletedAt,
		}
		if kind == forgeauth.Forgejo {
			step.StartedAt, step.FinishedAt = v.Started, v.Stopped
		}

		steps = append(steps, step)
	}

	return steps, nil
}
