package pipeprint

import (
	"errors"
	"fmt"
	"strings"
)

// Problem is one reason a pipeline is invalid.
type Problem struct {
	// Path locates the field, such as jobs.build.needs.
	Path string
	// Message says what is wrong.
	Message string
}

// String returns "path: message", or the message alone without a path.
func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}

	return p.Path + ": " + p.Message
}

// ValidationError lists why a pipeline is invalid. It wraps ErrInvalid.
type ValidationError struct {
	Problems []Problem
}

// Error lists the problems, one per line.
func (e *ValidationError) Error() string {
	lines := []string{ErrInvalid.Error()}
	for _, problem := range e.Problems {
		lines = append(lines, problem.String())
	}

	return strings.Join(lines, "\n")
}

// Unwrap returns ErrInvalid.
func (e *ValidationError) Unwrap() error {
	return ErrInvalid
}

// Validate returns a *ValidationError listing every problem, or nil when p
// is valid.
func (p *Pipeline) Validate() error {
	problems := p.Problems()
	if len(problems) == 0 {
		return nil
	}

	return &ValidationError{Problems: problems}
}

// ProblemsOf returns the problems in err: those of a *ValidationError, or
// err itself as one problem.
func ProblemsOf(err error) []Problem {
	if err == nil {
		return nil
	}

	var invalid *ValidationError
	if errors.As(err, &invalid) {
		return invalid.Problems
	}

	return []Problem{{Message: err.Error()}}
}

// Problems lists every reason p is invalid, in file order.
func (p *Pipeline) Problems() []Problem {
	v := &validator{}
	if p.Pipeprint != Version {
		v.add("pipeprint", "must be "+Version)
	}

	if len(p.Jobs) == 0 {
		v.add("jobs", "need at least one job")
	}

	v.native("native", p.Native)
	v.triggers(p.Triggers)
	v.jobs(p.Jobs)

	return v.problems
}

// validator collects problems.
type validator struct {
	problems []Problem
}

func (v *validator) add(path, message string) {
	v.problems = append(v.problems, Problem{Path: path, Message: message})
}

// native checks that every native block uses a known dialect.
func (v *validator) native(path string, n Native) {
	for dialect := range n {
		if !known(dialect) {
			v.add(path, fmt.Sprintf("unknown dialect %q", dialect))
		}
	}
}

func known(d Dialect) bool {
	for _, dialect := range Dialects() {
		if d == dialect {
			return true
		}
	}

	return false
}

func (v *validator) triggers(t Triggers) {
	v.native("triggers.native", t.Native)
	if t.Push != nil {
		v.native("triggers.push.native", t.Push.Native)
	}

	if t.PullRequest != nil {
		v.native("triggers.pull_request.native", t.PullRequest.Native)
	}

	if t.Manual != nil {
		v.native("triggers.manual.native", t.Manual.Native)
	}
}

// jobs checks each job, then the needs between them.
func (v *validator) jobs(jobs []Job) {
	ids := map[string]bool{}
	for _, job := range jobs {
		path := "jobs." + job.ID
		switch {
		case strings.TrimSpace(job.ID) == "":
			v.add("jobs", "every job needs an id")
		case strings.TrimSpace(job.ID) != job.ID:
			v.add(path, "id must not start or end with spaces")
		case ids[job.ID]:
			v.add(path, "duplicate job id")
		}

		ids[job.ID] = true
		v.job(path, job)
	}

	for _, job := range jobs {
		v.needs(job, ids)
	}

	if cycle := findCycle(jobs); cycle != "" {
		v.add("jobs."+cycle+".needs", "needs form a cycle")
	}
}

func (v *validator) job(path string, job Job) {
	v.native(path+".native", job.Native)
	for dialect := range job.If {
		if !known(dialect) {
			v.add(path+".if", fmt.Sprintf("unknown dialect %q", dialect))
		}
	}

	if job.TimeoutMinutes < 0 {
		v.add(path+".timeout_minutes", "must not be negative")
	}

	if job.Environment != nil && job.Environment.Name == "" {
		v.add(path+".environment.name", "is required")
	}

	for axis, values := range job.Matrix {
		if len(values) == 0 {
			v.add(path+".matrix."+axis, "needs at least one value")
		}
	}

	for i, service := range job.Services {
		v.service(fmt.Sprintf("%s.services[%d]", path, i), service)
	}

	for i, step := range job.Steps {
		v.step(fmt.Sprintf("%s.steps[%d]", path, i), step)
	}
}

func (v *validator) service(path string, s Service) {
	v.native(path+".native", s.Native)
	if s.Image == "" {
		v.add(path+".image", "is required")
	}
}

func (v *validator) step(path string, s Step) {
	v.native(path+".native", s.Native)

	kinds := 0
	for _, set := range []bool{s.Checkout, s.Uses != "", len(s.Run) > 0} {
		if set {
			kinds++
		}
	}

	if kinds > 1 {
		v.add(path, "set only one of checkout, uses and run")
	}

	if kinds == 0 && len(s.Native) == 0 {
		v.add(path, "set checkout, uses or run")
	}

	if len(s.With) > 0 && s.Uses == "" {
		v.add(path+".with", "needs uses")
	}
}

func (v *validator) needs(job Job, ids map[string]bool) {
	for _, need := range job.Needs {
		path := "jobs." + job.ID + ".needs"
		switch {
		case need == job.ID:
			v.add(path, "a job cannot need itself")
		case !ids[need]:
			v.add(path, fmt.Sprintf("unknown job %q", need))
		}
	}
}
