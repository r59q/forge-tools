package gitlab

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/portable"
	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	"go.yaml.in/yaml/v3"
)

// job reads one job and reports whether it had a needs keyword.
func (r *reader) job(pair yamlnode.Pair, value *yaml.Node) (pipeprint.Job, bool) {
	pairs, _ := yamlnode.Pairs(value)
	f := yamlnode.NewFields(pairs)
	path := "jobs." + pair.Key

	job := pipeprint.Job{ID: pair.Key}
	job.Stage = f.TakeString("stage")
	job.RunsOn = f.TakeStrings("tags")
	job.Image = portable.Image(f, "image", "name")
	job.Services = takeServices(f)
	job.Env = f.TakeStringMap("variables")
	job.If = takeCondition(f)
	job.TimeoutMinutes = takeTimeout(f)
	job.ContinueOnError = f.TakeBool("allow_failure")
	job.Matrix = takeMatrix(f)
	job.Environment = portable.Environment(f, "environment")

	hasNeeds := false
	if v := f.Take("needs"); v != nil {
		job.Needs, hasNeeds = r.needs(path, v)
		if !hasNeeds {
			f.Keep("needs")
		}
	}

	job.Steps = steps(f, job.Env)
	portable.SetNative(&job.Native, dialect, f.Rest())

	return job, hasNeeds
}

// needs reads job names from needs entries: names or {job: name} maps.
// Options such as artifacts are dropped with a warning.
func (r *reader) needs(path string, n *yaml.Node) ([]string, bool) {
	n = yamlnode.Resolve(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil, false
	}

	needs := []string{}
	for _, item := range n.Content {
		if name, ok := yamlnode.AsString(item); ok {
			needs = append(needs, name)
			continue
		}

		pairs, _ := yamlnode.Pairs(item)
		name, ok := yamlnode.AsString(yamlnode.Get(pairs, "job"))
		if !ok {
			return nil, false
		}

		if len(pairs) > 1 {
			r.warnings.Add(path+".needs", "options of "+strconv.Quote(name)+" dropped")
		}

		needs = append(needs, name)
	}

	return needs, true
}

func takeServices(f *yamlnode.Fields) []pipeprint.Service {
	n := yamlnode.Resolve(f.Take("services"))
	if n == nil {
		return nil
	}

	if n.Kind != yaml.SequenceNode {
		f.Keep("services")
		return nil
	}

	services := make([]pipeprint.Service, 0, len(n.Content))
	for _, item := range n.Content {
		service, ok := parseService(item)
		if !ok {
			f.Keep("services")
			return nil
		}

		services = append(services, service)
	}

	return services
}

// parseService reads an image name or a {name, alias, variables} map.
func parseService(n *yaml.Node) (pipeprint.Service, bool) {
	if image, ok := yamlnode.AsString(n); ok {
		return pipeprint.Service{Image: image}, true
	}

	pairs, ok := yamlnode.Pairs(n)
	if !ok {
		return pipeprint.Service{}, false
	}

	f := yamlnode.NewFields(pairs)
	service := pipeprint.Service{Image: f.TakeString("name"), Name: f.TakeString("alias")}
	service.Env = f.TakeStringMap("variables")
	portable.SetNative(&service.Native, dialect, f.Rest())

	return service, service.Image != ""
}

// takeCondition reads rules when they are one plain if.
func takeCondition(f *yamlnode.Fields) pipeprint.Condition {
	n := yamlnode.Resolve(f.Take("rules"))
	if n != nil && n.Kind == yaml.SequenceNode && len(n.Content) == 1 {
		pairs, _ := yamlnode.Pairs(n.Content[0])
		if cond, ok := yamlnode.AsString(yamlnode.Get(pairs, "if")); ok && len(pairs) == 1 {
			return pipeprint.Condition{dialect: cond}
		}
	}

	if n != nil {
		f.Keep("rules")
	}

	return nil
}

func takeTimeout(f *yamlnode.Fields) int {
	v := f.Take("timeout")
	if v == nil {
		return 0
	}

	text, _ := yamlnode.AsString(v)
	minutes, ok := parseDuration(text)
	if !ok {
		f.Keep("timeout")
	}

	return minutes
}

// parseDuration reads hour and minute durations such as 1h 30m or
// 2 hours, in whole minutes.
func parseDuration(text string) (int, bool) {
	tokens := splitDuration(text)
	if len(tokens) == 0 || len(tokens)%2 != 0 {
		return 0, false
	}

	total := 0
	for i := 0; i < len(tokens); i += 2 {
		n, err := strconv.Atoi(tokens[i])
		scale, ok := unitMinutes(tokens[i+1])
		if err != nil || !ok || n < 0 {
			return 0, false
		}

		total += n * scale
	}

	return total, total > 0
}

// splitDuration splits 1h30m and 1 h 30 min alike into number and unit
// tokens.
func splitDuration(text string) []string {
	var tokens []string
	start := 0
	for i := 1; i <= len(text); i++ {
		boundary := i == len(text) || isDigit(text[i]) != isDigit(text[i-1]) || text[i] == ' '
		if !boundary {
			continue
		}

		if token := strings.TrimSpace(text[start:i]); token != "" {
			tokens = append(tokens, token)
		}

		start = i
	}

	return tokens
}

func isDigit(c byte) bool {
	return unicode.IsDigit(rune(c))
}

func unitMinutes(unit string) (int, bool) {
	switch strings.ToLower(unit) {
	case "h", "hr", "hrs", "hour", "hours":
		return 60, true
	case "m", "min", "mins", "minute", "minutes":
		return 1, true
	}

	return 0, false
}

// takeMatrix reads parallel when it is one plain matrix entry.
func takeMatrix(f *yamlnode.Fields) map[string][]string {
	v := f.Take("parallel")
	if v == nil {
		return nil
	}

	pairs, _ := yamlnode.Pairs(v)
	entries := yamlnode.Resolve(yamlnode.Get(pairs, "matrix"))
	if len(pairs) == 1 && entries != nil && entries.Kind == yaml.SequenceNode && len(entries.Content) == 1 {
		if axes, ok := portable.MatrixAxes(entries.Content[0]); ok {
			return axes
		}
	}

	f.Keep("parallel")

	return nil
}

// steps turns the scripts into steps. GitLab checks out the repository
// first unless GIT_STRATEGY is none.
func steps(f *yamlnode.Fields, env map[string]string) []pipeprint.Step {
	before := f.TakeFlatStrings("before_script")
	script := f.TakeFlatStrings("script")
	after := f.TakeFlatStrings("after_script")

	var steps []pipeprint.Step
	if len(before)+len(script) > 0 && env["GIT_STRATEGY"] != "none" {
		steps = append(steps, pipeprint.Step{Checkout: true})
	}

	for _, commands := range [][]string{before, script} {
		if len(commands) > 0 {
			steps = append(steps, pipeprint.Step{Run: commands})
		}
	}

	if len(after) > 0 {
		steps = append(steps, pipeprint.Step{Run: after, Always: true})
	}

	return steps
}
