// Package github compiles GitHub Actions workflows to and from Pipeprint.
// Forgejo Actions uses the same workflow language, so this package serves
// both forges.
package github

import (
	"errors"
	"fmt"

	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/portable"
	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	"go.yaml.in/yaml/v3"
)

// dialect is the native block this package reads and writes.
const dialect = pipeprint.GitHub

// Parse reads a workflow file. Settings Pipeprint does not model are kept
// in native github blocks. Warnings report YAML tags, which are not kept.
func Parse(data []byte) (*pipeprint.Pipeline, pipeprint.Warnings, error) {
	pairs, err := yamlnode.Load(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", pipeprint.ErrInvalid, err)
	}

	p, err := workflow(yamlnode.NewFields(pairs))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", pipeprint.ErrInvalid, err)
	}

	if err := p.Validate(); err != nil {
		return nil, nil, err
	}

	var warnings pipeprint.Warnings
	for _, tag := range yamlnode.CustomTags(pairs) {
		warnings.Add("file", "YAML tag "+tag+" is not kept; it is written back as a plain value")
	}

	return p, warnings, nil
}

func workflow(f *yamlnode.Fields) (*pipeprint.Pipeline, error) {
	p := &pipeprint.Pipeline{Pipeprint: pipeprint.Version}
	p.Name = f.TakeString("name")

	if v := f.Take("on"); v != nil {
		triggers, ok := parseTriggers(v)
		if !ok {
			f.Keep("on")
		}

		p.Triggers = triggers
	}

	p.Env = f.TakeStringMap("env")

	jobs, ok := yamlnode.Pairs(f.Take("jobs"))
	if !ok {
		return nil, errors.New("jobs must be a mapping")
	}

	for _, pair := range jobs {
		job, err := parseJob(pair)
		if err != nil {
			return nil, err
		}

		p.Jobs = append(p.Jobs, job)
	}

	portable.SetNative(&p.Native, dialect, f.Rest())

	return p, nil
}

// keepUnless returns key to the native rest when it could not be modeled.
func keepUnless(f *yamlnode.Fields, key string, ok bool) {
	if !ok {
		f.Keep(key)
	}
}

// fields returns the mapping entries of n, or an error naming what.
func fields(n *yaml.Node, what string) (*yamlnode.Fields, error) {
	if yamlnode.IsNull(n) {
		return yamlnode.NewFields(nil), nil
	}

	pairs, ok := yamlnode.Pairs(n)
	if !ok {
		return nil, fmt.Errorf("%s must be a mapping", what)
	}

	return yamlnode.NewFields(pairs), nil
}
