// Package compile reads and writes pipelines in every supported format and
// converts between them through Pipeprint.
package compile

import (
	"errors"
	"fmt"

	"github.com/r59q/forge-tools/forge-pipeprint/library/github"
	"github.com/r59q/forge-tools/forge-pipeprint/library/gitlab"
	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
)

// Format names a pipeline file format.
type Format string

// The formats.
const (
	// Pipeprint is Pipeprint YAML.
	Pipeprint Format = "pipeprint"
	// PipeprintJSON is Pipeprint JSON. Reading it is the same as Pipeprint.
	PipeprintJSON Format = "pipeprint-json"
	// GitHub is a GitHub Actions workflow.
	GitHub Format = "github"
	// Forgejo is a Forgejo Actions workflow.
	Forgejo Format = "forgejo"
	// GitLab is a GitLab CI/CD pipeline.
	GitLab Format = "gitlab"
)

// ErrFormat means a format is unknown or cannot be detected.
var ErrFormat = errors.New("compile: unknown format")

// Info describes a format.
type Info struct {
	// Format is the format's name.
	Format Format
	// Title is a display name.
	Title string
	// Path is where the forge reads the file, with * for any name.
	Path string
}

// Formats lists every format.
func Formats() []Info {
	return []Info{
		{Format: Pipeprint, Title: "Pipeprint YAML", Path: "*.pipeprint.yml"},
		{Format: PipeprintJSON, Title: "Pipeprint JSON", Path: "*.pipeprint.json"},
		{Format: GitHub, Title: "GitHub Actions", Path: ".github/workflows/*.yml"},
		{Format: Forgejo, Title: "Forgejo Actions", Path: ".forgejo/workflows/*.yml"},
		{Format: GitLab, Title: "GitLab CI/CD", Path: ".gitlab-ci.yml"},
	}
}

// Detect guesses a file's format: Pipeprint has a pipeprint key, workflows
// have on and jobs, and anything else is GitLab. Workflows are reported as
// GitHub; Forgejo reads the same.
func Detect(data []byte) (Format, error) {
	pairs, err := yamlnode.Load(data)
	if err != nil {
		return "", fmt.Errorf("%w: %w", pipeprint.ErrInvalid, err)
	}

	has := func(key string) bool { return yamlnode.Get(pairs, key) != nil }
	switch {
	case has("pipeprint"):
		return Pipeprint, nil
	case has("on") && has("jobs"):
		return GitHub, nil
	}

	return GitLab, nil
}

// Read parses data in format. The empty format detects it.
func Read(format Format, data []byte) (*pipeprint.Pipeline, pipeprint.Warnings, error) {
	if format == "" {
		detected, err := Detect(data)
		if err != nil {
			return nil, nil, err
		}

		format = detected
	}

	switch format {
	case Pipeprint, PipeprintJSON:
		p, err := pipeprint.Parse(data)
		return p, nil, err
	case GitHub, Forgejo:
		return github.Parse(data)
	case GitLab:
		return gitlab.Parse(data)
	}

	return nil, nil, ErrFormat
}

// Write writes p in format. Warnings report what the format cannot hold.
func Write(format Format, p *pipeprint.Pipeline) ([]byte, pipeprint.Warnings, error) {
	switch format {
	case Pipeprint:
		out, err := p.YAML()
		return out, nil, err
	case PipeprintJSON:
		out, err := p.JSON()
		return out, nil, err
	case GitHub, Forgejo:
		return github.Emit(p)
	case GitLab:
		return gitlab.Emit(p)
	}

	return nil, nil, ErrFormat
}

// Convert reads data in from, which may be empty to detect it, and writes
// it in to. Warnings from both steps are returned in order.
func Convert(from, to Format, data []byte) ([]byte, pipeprint.Warnings, error) {
	if !to.Known() {
		return nil, nil, ErrFormat
	}

	p, readWarnings, err := Read(from, data)
	if err != nil {
		return nil, nil, err
	}

	out, writeWarnings, err := Write(to, p)
	if err != nil {
		return nil, nil, err
	}

	return out, append(readWarnings, writeWarnings...), nil
}

// Check reads data in format, which may be empty to detect it, and returns
// the format and every problem. Valid files have no problems.
func Check(format Format, data []byte) (Format, []pipeprint.Problem) {
	if format == "" {
		detected, err := Detect(data)
		if err != nil {
			return "", pipeprint.ProblemsOf(err)
		}

		format = detected
	}

	_, _, err := Read(format, data)

	return format, pipeprint.ProblemsOf(err)
}

// Known reports whether f is a supported format.
func (f Format) Known() bool {
	for _, info := range Formats() {
		if info.Format == f {
			return true
		}
	}

	return false
}
