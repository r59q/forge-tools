package github

import (
	"strings"

	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/portable"
	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	"go.yaml.in/yaml/v3"
)

// checkoutAction is the action that checks out the repository.
const checkoutAction = "actions/checkout"

func parseJob(pair yamlnode.Pair) (pipeprint.Job, error) {
	f, err := fields(pair.Value, "job "+pair.Key)
	if err != nil {
		return pipeprint.Job{}, err
	}

	job := pipeprint.Job{ID: pair.Key}
	job.Name = f.TakeString("name")
	job.Needs = f.TakeStrings("needs")
	job.RunsOn = f.TakeStrings("runs-on")
	job.Image = portable.Image(f, "container", "image")
	job.Services = takeServices(f)
	job.Env = f.TakeStringMap("env")
	job.Environment = portable.Environment(f, "environment")
	job.Matrix = takeMatrix(f)

	if cond := f.TakeString("if"); cond != "" {
		job.If = pipeprint.Condition{dialect: cond}
	}

	if v := f.Take("timeout-minutes"); v != nil {
		minutes, ok := yamlnode.AsInt(v)
		keepUnless(f, "timeout-minutes", ok && minutes > 0)
		job.TimeoutMinutes = max(minutes, 0)
	}

	job.ContinueOnError = f.TakeBool("continue-on-error")

	if v := f.Take("steps"); v != nil {
		steps, ok := parseSteps(v)
		keepUnless(f, "steps", ok)
		job.Steps = steps
	}

	portable.SetNative(&job.Native, dialect, f.Rest())

	return job, nil
}

func takeServices(f *yamlnode.Fields) []pipeprint.Service {
	v := f.Take("services")
	if v == nil {
		return nil
	}

	pairs, ok := yamlnode.Pairs(v)
	if !ok {
		f.Keep("services")
		return nil
	}

	services := make([]pipeprint.Service, 0, len(pairs))
	for _, pair := range pairs {
		service, ok := parseService(pair)
		if !ok {
			f.Keep("services")
			return nil
		}

		services = append(services, service)
	}

	return services
}

func parseService(pair yamlnode.Pair) (pipeprint.Service, bool) {
	sf, err := fields(pair.Value, "service")
	if err != nil {
		return pipeprint.Service{}, false
	}

	service := pipeprint.Service{Name: pair.Key}
	service.Image = sf.TakeString("image")
	service.Env = sf.TakeStringMap("env")
	portable.SetNative(&service.Native, dialect, sf.Rest())

	return service, service.Image != ""
}

// takeMatrix reads strategy when it holds only plain matrix axes.
func takeMatrix(f *yamlnode.Fields) map[string][]string {
	v := f.Take("strategy")
	if v == nil {
		return nil
	}

	pairs, _ := yamlnode.Pairs(v)
	axes, ok := portable.MatrixAxes(yamlnode.Get(pairs, "matrix"), "include", "exclude")
	if !ok || len(pairs) != 1 {
		f.Keep("strategy")
		return nil
	}

	return axes
}

func parseSteps(n *yaml.Node) ([]pipeprint.Step, bool) {
	n = yamlnode.Resolve(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil, false
	}

	steps := make([]pipeprint.Step, 0, len(n.Content))
	for _, item := range n.Content {
		step, ok := parseStep(item)
		if !ok {
			return nil, false
		}

		steps = append(steps, step)
	}

	return steps, true
}

func parseStep(n *yaml.Node) (pipeprint.Step, bool) {
	f, err := fields(n, "step")
	if err != nil {
		return pipeprint.Step{}, false
	}

	step := pipeprint.Step{Name: f.TakeString("name"), Uses: f.TakeString("uses")}
	step.With = f.TakeStringMap("with")
	if run := f.TakeString("run"); run != "" {
		step.Run = pipeprint.Commands{run}
	}

	if v := f.Take("if"); v != nil {
		cond, _ := yamlnode.AsString(v)
		step.Always = isAlways(cond)
		keepUnless(f, "if", step.Always)
	}

	portable.SetNative(&step.Native, dialect, f.Rest())

	return step, true
}

// isAlways reports whether a step condition is always().
func isAlways(cond string) bool {
	cond = strings.TrimSpace(cond)
	if inner, ok := strings.CutPrefix(cond, "${{"); ok {
		cond = strings.TrimSpace(strings.TrimSuffix(inner, "}}"))
	}

	return cond == "always()"
}
