// Package pipeprint defines Pipeprint, one pipeline model shared by every
// forge. Portable fields describe what every forge can do. Native blocks keep
// the rest per dialect, so a file can round-trip without losing settings.
package pipeprint

// Version is the Pipeprint version this package reads and writes.
const Version = "v1"

// Dialect names a pipeline language family.
type Dialect string

// The dialects. GitHub covers GitHub Actions and Forgejo Actions.
const (
	GitHub Dialect = "github"
	GitLab Dialect = "gitlab"
)

// Dialects lists every dialect in a stable order.
func Dialects() []Dialect {
	return []Dialect{GitHub, GitLab}
}

// Native holds settings that only one dialect understands, keyed by dialect
// and then by the dialect's own key. Values are plain YAML values.
type Native map[Dialect]map[string]any

// Condition is a job condition per dialect, because expression languages
// differ. A forge uses its own dialect's expression or none.
type Condition map[Dialect]string

// Pipeline is one pipeline definition, such as one GitHub workflow file or
// one .gitlab-ci.yml.
type Pipeline struct {
	// Pipeprint is the format version, always Version.
	Pipeprint string `yaml:"pipeprint" json:"pipeprint"`
	// Name is the pipeline's display name.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Triggers says which events start the pipeline.
	Triggers Triggers `yaml:"triggers,omitempty" json:"triggers,omitzero"`
	// Env holds variables for every job.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// Stages orders stages on forges that have them. Needs carry the order
	// everywhere else.
	Stages []string `yaml:"stages,omitempty" json:"stages,omitempty"`
	// Jobs run in the order their needs allow.
	Jobs []Job `yaml:"jobs" json:"jobs"`
	// Native holds pipeline settings per dialect.
	Native Native `yaml:"native,omitempty" json:"native,omitempty"`
}

// Triggers are the events that start a pipeline.
type Triggers struct {
	// Push runs on pushed branches and tags.
	Push *Push `yaml:"push,omitempty" json:"push,omitempty"`
	// PullRequest runs on pull and merge requests.
	PullRequest *PullRequest `yaml:"pull_request,omitempty" json:"pull_request,omitempty"`
	// Schedule runs on cron schedules.
	Schedule []Schedule `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	// Manual runs when started by hand.
	Manual *Manual `yaml:"manual,omitempty" json:"manual,omitempty"`
	// Native holds other events per dialect.
	Native Native `yaml:"native,omitempty" json:"native,omitempty"`
}

// Push filters push events. Empty filters match every push.
type Push struct {
	// Branches are branch name globs.
	Branches []string `yaml:"branches,omitempty" json:"branches,omitempty"`
	// Tags are tag name globs.
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	// Paths are changed file globs.
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	// Native holds other filters per dialect.
	Native Native `yaml:"native,omitempty" json:"native,omitempty"`
}

// PullRequest filters pull or merge request events.
type PullRequest struct {
	// Branches are target branch name globs.
	Branches []string `yaml:"branches,omitempty" json:"branches,omitempty"`
	// Paths are changed file globs.
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	// Native holds other filters per dialect.
	Native Native `yaml:"native,omitempty" json:"native,omitempty"`
}

// Schedule is one cron schedule. Cron is empty when the forge keeps
// schedules outside the pipeline file.
type Schedule struct {
	Cron string `yaml:"cron,omitempty" json:"cron,omitempty"`
}

// Manual allows starting the pipeline by hand.
type Manual struct {
	// Native holds inputs and other settings per dialect.
	Native Native `yaml:"native,omitempty" json:"native,omitempty"`
}

// Job is one unit of work on one runner.
type Job struct {
	// ID names the job in needs. It is unique in the pipeline.
	ID string `yaml:"id" json:"id"`
	// Name is the display name.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Stage is the job's stage on forges that have them.
	Stage string `yaml:"stage,omitempty" json:"stage,omitempty"`
	// Needs are the IDs of jobs that must finish first.
	Needs []string `yaml:"needs,omitempty" json:"needs,omitempty"`
	// RunsOn are runner labels.
	RunsOn []string `yaml:"runs_on,omitempty" json:"runs_on,omitempty"`
	// Image is the container image the steps run in.
	Image string `yaml:"image,omitempty" json:"image,omitempty"`
	// Services are containers that run beside the job.
	Services []Service `yaml:"services,omitempty" json:"services,omitempty"`
	// Env holds job variables.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// If runs the job only when its dialect's expression holds.
	If Condition `yaml:"if,omitempty" json:"if,omitempty"`
	// TimeoutMinutes bounds the job. Zero uses the forge default.
	TimeoutMinutes int `yaml:"timeout_minutes,omitempty" json:"timeout_minutes,omitempty"`
	// ContinueOnError lets the pipeline pass when the job fails.
	ContinueOnError bool `yaml:"continue_on_error,omitempty" json:"continue_on_error,omitempty"`
	// Matrix runs the job once per combination of axis values.
	Matrix map[string][]string `yaml:"matrix,omitempty" json:"matrix,omitempty"`
	// Environment is the deployment environment.
	Environment *Environment `yaml:"environment,omitempty" json:"environment,omitempty"`
	// Steps run in order in one workspace.
	Steps []Step `yaml:"steps,omitempty" json:"steps,omitempty"`
	// Native holds job settings per dialect.
	Native Native `yaml:"native,omitempty" json:"native,omitempty"`
}

// Service is a container that runs beside a job.
type Service struct {
	// Name is the host name the job reaches it by.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Image is the container image.
	Image string `yaml:"image" json:"image"`
	// Env holds the container's variables.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// Native holds service settings per dialect.
	Native Native `yaml:"native,omitempty" json:"native,omitempty"`
}

// Environment is a deployment environment.
type Environment struct {
	Name string `yaml:"name" json:"name"`
	URL  string `yaml:"url,omitempty" json:"url,omitempty"`
}

// Step is one action in a job. It sets at most one of Checkout, Uses and Run.
type Step struct {
	// Name is the display name.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Checkout checks out the pipeline's repository.
	Checkout bool `yaml:"checkout,omitempty" json:"checkout,omitempty"`
	// Uses is a reusable action, such as actions/setup-go@v5.
	Uses string `yaml:"uses,omitempty" json:"uses,omitempty"`
	// With holds the action's inputs.
	With map[string]string `yaml:"with,omitempty" json:"with,omitempty"`
	// Run holds shell commands that run in one shell session.
	Run Commands `yaml:"run,omitempty" json:"run,omitempty"`
	// Always runs the step even when earlier steps failed.
	Always bool `yaml:"always,omitempty" json:"always,omitempty"`
	// Native holds step settings per dialect.
	Native Native `yaml:"native,omitempty" json:"native,omitempty"`
}
