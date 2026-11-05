package github

import (
	"strings"

	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/portable"
	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	"go.yaml.in/yaml/v3"
)

// defaultRunner is used when a job names no runner.
const defaultRunner = "ubuntu-latest"

// checkoutUses is written for portable checkout steps.
const checkoutUses = checkoutAction + "@v4"

// Key orders for written mappings; "*" marks unlisted keys.
func workflowOrder() []string {
	return []string{"name", "run-name", "on", "permissions", "concurrency", "env", "defaults", "*", "jobs"}
}

func jobOrder() []string {
	return []string{
		"name", "needs", "if", "runs-on", "container", "services", "environment", "permissions",
		"concurrency", "outputs", "timeout-minutes", "continue-on-error", "strategy", "defaults", "env", "*", "steps",
	}
}

func stepOrder() []string {
	return []string{"name", "id", "if", "uses", "with", "run", "shell", "working-directory", "env", "*"}
}

// Emit writes p as a workflow file. Warnings report what was dropped or
// defaulted: gitlab settings, conditions without a github expression and
// missing runners.
func Emit(p *pipeprint.Pipeline) ([]byte, pipeprint.Warnings, error) {
	if err := p.Validate(); err != nil {
		return nil, nil, err
	}

	e := &emitter{ids: jobIDs(p.Jobs)}
	e.warnings.Foreign("pipeline", p.Native, dialect)

	m := yamlnode.NewMap()
	if p.Name != "" {
		m.Add("name", yamlnode.Str(p.Name))
	}

	m.Add("on", e.triggers(p.Triggers))
	m.Add("env", yamlnode.StringMap(p.Env))
	m.Add("jobs", e.jobs(p.Jobs))
	e.rest(m, "pipeline", p.Native)
	m.Sort(workflowOrder())

	out, err := yamlnode.Encode(m.Node)

	return out, e.warnings, err
}

// emitter collects warnings while writing.
type emitter struct {
	warnings pipeprint.Warnings
	// ids maps Pipeprint job IDs to valid workflow job IDs.
	ids map[string]string
}

// rest adds n's github settings to m and warns about conflicts.
func (e *emitter) rest(m *yamlnode.Map, path string, n pipeprint.Native) {
	for _, key := range m.AddRest(n[dialect]) {
		e.warnings.Add(path+".native.github."+key, "ignored; a portable field sets it")
	}
}

func (e *emitter) jobs(jobs []pipeprint.Job) *yaml.Node {
	m := yamlnode.NewMap()
	for _, job := range jobs {
		m.Add(e.ids[job.ID], e.job(job))
	}

	return m.Node
}

func (e *emitter) job(job pipeprint.Job) *yaml.Node {
	path := "jobs." + job.ID
	e.warnings.Foreign(path, job.Native, dialect)

	m := yamlnode.NewMap()
	m.Add("name", e.jobName(job))
	m.Add("needs", e.needs(job.Needs))
	m.Add("if", e.condition(path, job.If))
	m.Add("runs-on", e.runsOn(path, job))
	if job.Image != "" {
		m.Add("container", yamlnode.Str(job.Image))
	}

	m.Add("services", services(job.Services, &e.warnings, path))
	m.Add("environment", portable.EnvironmentNode(job.Environment))
	if job.TimeoutMinutes > 0 {
		m.Add("timeout-minutes", yamlnode.Int(job.TimeoutMinutes))
	}

	if job.ContinueOnError {
		m.Add("continue-on-error", yamlnode.Bool(true))
	}

	m.Add("strategy", matrix(job.Matrix))
	m.Add("env", yamlnode.StringMap(job.Env))
	m.Add("steps", e.steps(path, job.Steps))
	e.rest(m, path, job.Native)
	m.Sort(jobOrder())

	return m.Node
}

// jobName keeps the original ID as the name when the ID had to change.
func (e *emitter) jobName(job pipeprint.Job) *yaml.Node {
	switch {
	case job.Name != "":
		return yamlnode.Str(job.Name)
	case e.ids[job.ID] != job.ID:
		return yamlnode.Str(job.ID)
	}

	return nil
}

func (e *emitter) needs(needs []string) *yaml.Node {
	if len(needs) == 0 {
		return nil
	}

	ids := make([]string, len(needs))
	for i, need := range needs {
		ids[i] = e.ids[need]
	}

	return yamlnode.Strings(ids)
}

// condition returns the github expression, warning when only other
// dialects have one.
func (e *emitter) condition(path string, c pipeprint.Condition) *yaml.Node {
	if cond, ok := c[dialect]; ok {
		return yamlnode.Str(cond)
	}

	if len(c) > 0 {
		e.warnings.Add(path+".if", "no github expression; the job runs unconditionally")
	}

	return nil
}

func (e *emitter) runsOn(path string, job pipeprint.Job) *yaml.Node {
	if len(job.RunsOn) > 0 {
		return yamlnode.Strings(job.RunsOn)
	}

	// Jobs that call a reusable workflow take no runner.
	native := job.Native[dialect]
	if _, ok := native["runs-on"]; ok {
		return nil
	}

	if _, ok := native["uses"]; ok {
		return nil
	}

	e.warnings.Add(path+".runs_on", "no runner; defaulted to "+defaultRunner)

	return yamlnode.Str(defaultRunner)
}

func services(list []pipeprint.Service, w *pipeprint.Warnings, path string) *yaml.Node {
	if len(list) == 0 {
		return nil
	}

	m := yamlnode.NewMap()
	for _, s := range list {
		w.Foreign(path+".services."+s.Name, s.Native, dialect)

		service := yamlnode.NewMap()
		service.Add("image", yamlnode.Str(s.Image))
		service.Add("env", yamlnode.StringMap(s.Env))
		service.AddRest(s.Native[dialect])
		m.Add(serviceName(s), service.Node)
	}

	return m.Node
}

// serviceName returns the service's name, or one derived from its image,
// such as postgres for postgres:16.
func serviceName(s pipeprint.Service) string {
	if s.Name != "" {
		return s.Name
	}

	name := s.Image[strings.LastIndex(s.Image, "/")+1:]
	name, _, _ = strings.Cut(name, ":")
	name, _, _ = strings.Cut(name, "@")

	return name
}

func matrix(axes map[string][]string) *yaml.Node {
	node := portable.AxesNode(axes)
	if node == nil {
		return nil
	}

	strategy := yamlnode.NewMap()
	strategy.Add("matrix", node)

	return strategy.Node
}
