// Package gitlab compiles GitLab CI/CD pipelines (.gitlab-ci.yml) to and
// from Pipeprint.
package gitlab

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/portable"
	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	"go.yaml.in/yaml/v3"
)

// dialect is the native block this package reads and writes.
const dialect = pipeprint.GitLab

// keywords are top-level keys that are not jobs.
func keywords() []string {
	return []string{
		"default", "include", "stages", "variables", "workflow", "image", "services",
		"cache", "before_script", "after_script", "types", "spec",
	}
}

// Parse reads a .gitlab-ci.yml. Hidden jobs, includes, defaults and other
// settings Pipeprint does not model are kept in native gitlab blocks.
// Warnings report what could not be kept or had to be assumed.
func Parse(data []byte) (*pipeprint.Pipeline, pipeprint.Warnings, error) {
	pairs, err := yamlnode.Load(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", pipeprint.ErrInvalid, err)
	}

	if len(pairs) == 1 && pairs[0].Key == "spec" {
		return nil, nil, fmt.Errorf("%w: spec headers are not supported", pipeprint.ErrInvalid)
	}

	r := &reader{}
	for _, tag := range yamlnode.CustomTags(pairs) {
		r.warnings.Add("file", "YAML tag "+tag+" is not kept; it is written back as a plain value")
	}

	p, err := r.pipeline(pairs)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", pipeprint.ErrInvalid, err)
	}

	if err := p.Validate(); err != nil {
		return nil, nil, err
	}

	return p, r.warnings, nil
}

// reader collects warnings while reading.
type reader struct {
	warnings pipeprint.Warnings
}

func (r *reader) pipeline(pairs []yamlnode.Pair) (*pipeprint.Pipeline, error) {
	f := yamlnode.NewFields(pairs)
	p := &pipeprint.Pipeline{Pipeprint: pipeprint.Version}
	p.Stages = f.TakeStrings("stages")
	p.Env = f.TakeStringMap("variables")
	p.Triggers = pipeprint.Triggers{Push: &pipeprint.Push{}}
	r.workflow(f, p)

	explicit := map[string]bool{}
	for _, pair := range pairs {
		if !isJob(pair) {
			continue
		}

		job, hasNeeds := r.job(pair, f.Take(pair.Key))
		explicit[job.ID] = hasNeeds
		p.Jobs = append(p.Jobs, job)
	}

	if len(p.Jobs) == 0 {
		return nil, errors.New("no jobs")
	}

	r.finishNeeds(p, explicit)
	portable.SetNative(&p.Native, dialect, f.Rest())

	return p, nil
}

// isJob reports whether a top-level entry is a visible job.
func isJob(pair yamlnode.Pair) bool {
	_, isMap := yamlnode.Pairs(pair.Value)
	return isMap && !strings.HasPrefix(pair.Key, ".") && !slices.Contains(keywords(), pair.Key)
}

// workflow reads the name and the rules it understands. The rest stays
// native.
func (r *reader) workflow(f *yamlnode.Fields, p *pipeprint.Pipeline) {
	v := f.Take("workflow")
	if v == nil {
		return
	}

	pairs, ok := yamlnode.Pairs(v)
	if !ok {
		f.Keep("workflow")
		return
	}

	wf := yamlnode.NewFields(pairs)
	p.Name = wf.TakeString("name")
	if rules := wf.Take("rules"); rules != nil {
		r.triggers(wf, rules, p)
	}

	if rest := wf.Rest(); rest != nil {
		p.Native.Set(dialect, "workflow", rest)
	}
}

func (r *reader) triggers(wf *yamlnode.Fields, rules *yaml.Node, p *pipeprint.Pipeline) {
	triggers, ok := parseRules(rules)
	if ok {
		p.Triggers = triggers
		return
	}

	wf.Keep("rules")
	r.warnings.Add("workflow.rules", "not understood; kept native and triggers assumed to be push")
}

// finishNeeds drops needs on jobs outside the file, such as included ones,
// and sets the stage order as needs for jobs without needs.
func (r *reader) finishNeeds(p *pipeprint.Pipeline, explicit map[string]bool) {
	for i := range p.Jobs {
		p.Jobs[i].Needs = r.knownNeeds(p, p.Jobs[i])
	}

	stageOf := map[string]string{}
	for _, job := range p.Jobs {
		stageOf[job.ID] = job.Stage
		if job.Stage == "" {
			stageOf[job.ID] = defaultStage
		}
	}

	implicit := implicitNeeds(p.Jobs, stageOf, stageOrder(p.Stages))
	for i := range p.Jobs {
		if !explicit[p.Jobs[i].ID] {
			p.Jobs[i].Needs = implicit[p.Jobs[i].ID]
		}
	}
}

func (r *reader) knownNeeds(p *pipeprint.Pipeline, job pipeprint.Job) []string {
	known := job.Needs[:0:0]
	for _, need := range job.Needs {
		if p.Job(need) == nil {
			r.warnings.Add("jobs."+job.ID+".needs", "unknown job "+strconv.Quote(need)+" dropped")
			continue
		}

		known = append(known, need)
	}

	if job.Needs == nil {
		return nil
	}

	return known
}
