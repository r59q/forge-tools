package github

import (
	"fmt"
	"strings"

	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	"go.yaml.in/yaml/v3"
)

func (e *emitter) steps(path string, steps []pipeprint.Step) *yaml.Node {
	if len(steps) == 0 {
		return nil
	}

	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for i, step := range steps {
		seq.Content = append(seq.Content, e.step(fmt.Sprintf("%s.steps[%d]", path, i), step))
	}

	return seq
}

func (e *emitter) step(path string, step pipeprint.Step) *yaml.Node {
	e.warnings.Foreign(path, step.Native, dialect)

	m := yamlnode.NewMap()
	if step.Name != "" {
		m.Add("name", yamlnode.Str(step.Name))
	}

	switch {
	case step.Checkout:
		m.Add("uses", yamlnode.Str(checkoutUses))
	case step.Uses != "":
		m.Add("uses", yamlnode.Str(step.Uses))
		m.Add("with", yamlnode.StringMap(step.With))
	case len(step.Run) > 0:
		run := strings.Join(step.Run, "\n")
		e.foreignVariables(path+".run", run)
		m.Add("run", yamlnode.Str(run))
	}

	native := step.Native
	if cond := always(step); cond != nil {
		m.Add("if", cond)
		native = without(native, "if")
	}

	e.rest(m, path, native)
	m.Sort(stepOrder())

	return m.Node
}

// always returns the condition for Always steps, combined with any github
// condition.
func always(step pipeprint.Step) *yaml.Node {
	if !step.Always {
		return nil
	}

	cond, ok := step.Native[dialect]["if"].(string)
	if !ok || cond == "" {
		return yamlnode.Str("always()")
	}

	cond = strings.TrimSpace(cond)
	if inner, found := strings.CutPrefix(cond, "${{"); found {
		cond = strings.TrimSpace(strings.TrimSuffix(inner, "}}"))
	}

	return yamlnode.Str("always() && (" + cond + ")")
}

// foreignVariables warns when commands use GitLab's predefined variables.
func (e *emitter) foreignVariables(path, commands string) {
	if strings.Contains(commands, "$CI_") || strings.Contains(commands, "${CI_") {
		e.warnings.Add(path, "uses GitLab CI_ variables, which are not translated")
	}
}

// without returns a copy of n without the github key.
func without(n pipeprint.Native, key string) pipeprint.Native {
	out := pipeprint.Native{}
	for d, values := range n {
		out[d] = values
	}

	kept := map[string]any{}
	for k, v := range n[dialect] {
		if k != key {
			kept[k] = v
		}
	}

	out[dialect] = kept

	return out
}
