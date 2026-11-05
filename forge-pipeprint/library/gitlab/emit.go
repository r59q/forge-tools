package gitlab

import (
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/portable"
	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	"go.yaml.in/yaml/v3"
)

// Key orders for written mappings; "*" marks unlisted keys.
func topOrder() []string {
	return []string{"workflow", "include", "stages", "variables", "default", "*"}
}

func jobOrder() []string {
	return []string{
		"stage", "needs", "extends", "image", "services", "tags", "variables", "rules", "environment",
		"parallel", "timeout", "allow_failure", "*", "before_script", "script", "after_script",
	}
}

// Emit writes p as a .gitlab-ci.yml. Warnings report what was dropped or
// changed: github settings, actions, step-level options, runner labels
// and schedules, which GitLab keeps in project settings.
func Emit(p *pipeprint.Pipeline) ([]byte, pipeprint.Warnings, error) {
	if err := p.Validate(); err != nil {
		return nil, nil, err
	}

	e := &emitter{}
	e.warnings.Foreign("pipeline", p.Native, dialect)
	pl := planStages(p, &e.warnings)

	top := yamlnode.NewMap()
	top.Add("workflow", e.workflow(p))
	if pl.stages != nil {
		top.Add("stages", yamlnode.Seq(pl.stages))
	}

	top.Add("variables", yamlnode.StringMap(p.Env))
	e.rest(top, "pipeline", without(p.Native[dialect], "workflow"))
	top.Sort(topOrder())

	implicit := implicitNeeds(p.Jobs, pl.stageOf, pl.order)
	for _, job := range p.Jobs {
		top.Add(job.ID, e.job(job, pl, implicit[job.ID]))
	}

	out, err := yamlnode.Encode(top.Node)

	return out, e.warnings, err
}

// emitter collects warnings while writing.
type emitter struct {
	warnings pipeprint.Warnings
}

// rest adds native settings to m and warns about conflicts.
func (e *emitter) rest(m *yamlnode.Map, path string, native map[string]any) {
	for _, key := range m.AddRest(native) {
		e.warnings.Add(path+".native.gitlab."+key, "ignored; a portable field sets it")
	}
}

// without returns a copy of m without key.
func without(m map[string]any, key string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}

	return out
}

// workflow writes the name, the trigger rules and native workflow
// settings, or nil when there are none.
func (e *emitter) workflow(p *pipeprint.Pipeline) *yaml.Node {
	m := yamlnode.NewMap()
	if p.Name != "" {
		m.Add("name", yamlnode.Str(p.Name))
	}

	m.Add("rules", e.rules(p.Triggers))
	if native, ok := p.Native[dialect]["workflow"].(map[string]any); ok {
		e.rest(m, "pipeline.native.gitlab.workflow", native)
	}

	if m.Len() == 0 {
		return nil
	}

	return m.Node
}

func (e *emitter) job(job pipeprint.Job, pl plan, implicit []string) *yaml.Node {
	path := "jobs." + job.ID
	e.warnings.Foreign(path, job.Native, dialect)

	m := yamlnode.NewMap()
	if pl.write[job.ID] {
		m.Add("stage", yamlnode.Str(pl.stageOf[job.ID]))
	}

	if !sameSet(job.Needs, implicit) {
		m.Add("needs", yamlnode.Seq(job.Needs))
	}

	if job.Image != "" {
		m.Add("image", yamlnode.Str(job.Image))
	}

	m.Add("services", services(job.Services, &e.warnings, path))
	m.Add("tags", e.tags(path, job.RunsOn))
	m.Add("variables", yamlnode.StringMap(job.Env))
	m.Add("rules", e.condition(path, job.If))
	m.Add("environment", portable.EnvironmentNode(job.Environment))
	m.Add("parallel", matrix(job.Matrix))
	if job.TimeoutMinutes > 0 {
		m.Add("timeout", yamlnode.Str(duration(job.TimeoutMinutes)))
	}

	if job.ContinueOnError {
		m.Add("allow_failure", yamlnode.Bool(true))
	}

	e.scripts(m, path, job)
	e.rest(m, path, job.Native[dialect])
	m.Sort(jobOrder())

	return m.Node
}

// condition writes a gitlab expression as one rule, warning when only
// other dialects have one.
func (e *emitter) condition(path string, c pipeprint.Condition) *yaml.Node {
	cond, ok := c[dialect]
	if !ok {
		if len(c) > 0 {
			e.warnings.Add(path+".if", "no gitlab expression; the job runs unconditionally")
		}

		return nil
	}

	rule := yamlnode.NewMap()
	rule.Add("if", yamlnode.Str(cond))

	return &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{rule.Node}}
}

// tags writes runner labels, dropping GitHub-hosted runner labels.
func (e *emitter) tags(path string, labels []string) *yaml.Node {
	var tags []string
	for _, label := range labels {
		if hostedLabel(label) {
			e.warnings.Add(path+".runs_on", "GitHub-hosted runner "+strconv.Quote(label)+" dropped")
			continue
		}

		tags = append(tags, label)
	}

	if len(tags) == 0 {
		return nil
	}

	return yamlnode.Seq(tags)
}

func hostedLabel(label string) bool {
	for _, prefix := range []string{"ubuntu-", "windows-", "macos-"} {
		if strings.HasPrefix(label, prefix) {
			return true
		}
	}

	return label == "self-hosted"
}

func services(list []pipeprint.Service, w *pipeprint.Warnings, path string) *yaml.Node {
	if len(list) == 0 {
		return nil
	}

	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for i, s := range list {
		w.Foreign(path+".services["+strconv.Itoa(i)+"]", s.Native, dialect)
		if s.Name == "" && len(s.Env) == 0 && len(s.Native[dialect]) == 0 {
			seq.Content = append(seq.Content, yamlnode.Str(s.Image))
			continue
		}

		m := yamlnode.NewMap()
		m.Add("name", yamlnode.Str(s.Image))
		if s.Name != "" {
			m.Add("alias", yamlnode.Str(s.Name))
		}

		m.Add("variables", yamlnode.StringMap(s.Env))
		m.AddRest(s.Native[dialect])
		seq.Content = append(seq.Content, m.Node)
	}

	return seq
}

func matrix(axes map[string][]string) *yaml.Node {
	node := portable.AxesNode(axes)
	if node == nil {
		return nil
	}

	m := yamlnode.NewMap()
	m.Add("matrix", &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{node}})

	return m.Node
}

// duration writes minutes as GitLab does, such as 1h 30m.
func duration(minutes int) string {
	hours, rest := minutes/60, minutes%60
	switch {
	case hours == 0:
		return strconv.Itoa(rest) + "m"
	case rest == 0:
		return strconv.Itoa(hours) + "h"
	}

	return strconv.Itoa(hours) + "h " + strconv.Itoa(rest) + "m"
}
