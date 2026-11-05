package gitlab

import (
	"fmt"
	"strings"

	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
)

// checkoutAction is GitHub's checkout action. GitLab checks out the
// repository itself.
const checkoutAction = "actions/checkout@"

// noop is the script for jobs whose steps GitLab cannot run.
const noop = "true"

// scripts writes the steps as script and after_script. Checkout steps are
// implicit; actions cannot run and are dropped.
func (e *emitter) scripts(m *yamlnode.Map, path string, job pipeprint.Job) {
	var script, after []string
	for i, step := range job.Steps {
		stepPath := fmt.Sprintf("%s.steps[%d]", path, i)
		e.warnings.Foreign(stepPath, step.Native, dialect)

		commands := e.commands(stepPath, step)
		if !step.Always {
			script = append(script, commands...)
			continue
		}

		if i < len(job.Steps)-1 && !job.Steps[i+1].Always {
			e.warnings.Add(stepPath, "always step moved to after_script")
		}

		after = append(after, commands...)
	}

	if len(script) == 0 && !hasNative(job, "script", "trigger", "extends") {
		e.warnings.Add(path, "no commands GitLab can run; wrote a no-op script")
		script = []string{noop}
	}

	if len(script) > 0 {
		m.Add("script", yamlnode.Seq(script))
	}

	if len(after) > 0 {
		m.Add("after_script", yamlnode.Seq(after))
	}
}

// commands returns what a step runs on GitLab.
func (e *emitter) commands(path string, step pipeprint.Step) []string {
	switch {
	case step.Checkout:
		return nil
	case strings.HasPrefix(step.Uses, checkoutAction):
		if len(step.With) > 0 {
			e.warnings.Add(path+".with", "checkout options dropped; GitLab checks out the repository itself")
		}

		return nil
	case step.Uses != "":
		e.warnings.Add(path, "action "+step.Uses+" cannot run on GitLab; dropped")
		return nil
	}

	commands := make([]string, len(step.Run))
	for i, command := range step.Run {
		commands[i] = strings.TrimRight(command, "\n")
	}

	if strings.Contains(strings.Join(commands, "\n"), "${{") {
		e.warnings.Add(path+".run", "uses ${{ }} expressions, which are not translated")
	}

	return commands
}

// hasNative reports whether the job sets any of keys natively.
func hasNative(job pipeprint.Job, keys ...string) bool {
	for _, key := range keys {
		if _, ok := job.Native[dialect][key]; ok {
			return true
		}
	}

	return false
}
