package gitlab

import (
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	"go.yaml.in/yaml/v3"
)

// Predefined variables and pipeline sources that rules test.
const (
	varSource       = "$CI_PIPELINE_SOURCE"
	varBranch       = "$CI_COMMIT_BRANCH"
	varTag          = "$CI_COMMIT_TAG"
	varTargetBranch = "$CI_MERGE_REQUEST_TARGET_BRANCH_NAME"

	sourcePush         = "push"
	sourceMergeRequest = "merge_request_event"
	sourceSchedule     = "schedule"
	sourceManual       = "web"
)

// rule is one workflow rule: a pipeline source, an optional ref test and
// changed paths.
type rule struct {
	source  string
	ref     string
	changes []string
}

// isDefault reports whether t is GitLab's behavior without workflow
// rules: run on every push.
func isDefault(t pipeprint.Triggers) bool {
	push := t.Push
	return push != nil && len(push.Branches)+len(push.Tags)+len(push.Paths)+len(push.Native) == 0 &&
		t.PullRequest == nil && len(t.Schedule) == 0 && t.Manual == nil && len(t.Native) == 0
}

// rules returns the workflow rules for t, or nil for GitLab's default.
func (e *emitter) rules(t pipeprint.Triggers) *yaml.Node {
	e.foreignTriggers(t)
	if isDefault(t) {
		return nil
	}

	var rules []rule
	if t.Push != nil {
		rules = append(rules, e.refRules(sourcePush, t.Push.Paths, refs(varBranch, t.Push.Branches), refs(varTag, t.Push.Tags))...)
	}

	if t.PullRequest != nil {
		rules = append(rules, e.refRules(sourceMergeRequest, t.PullRequest.Paths, refs(varTargetBranch, t.PullRequest.Branches))...)
	}

	if len(t.Schedule) > 0 {
		e.dropCrons(t.Schedule)
		rules = append(rules, rule{source: sourceSchedule})
	}

	if t.Manual != nil {
		rules = append(rules, rule{source: sourceManual})
	}

	if len(rules) == 0 {
		e.warnings.Add("triggers", "no triggers; GitLab runs on every push")
		return nil
	}

	return rulesNode(rules)
}

func (e *emitter) foreignTriggers(t pipeprint.Triggers) {
	e.warnings.Foreign("triggers", t.Native, dialect)
	if t.Push != nil {
		e.warnings.Foreign("triggers.push", t.Push.Native, dialect)
	}

	if t.PullRequest != nil {
		e.warnings.Foreign("triggers.pull_request", t.PullRequest.Native, dialect)
	}

	if t.Manual != nil {
		e.warnings.Foreign("triggers.manual", t.Manual.Native, dialect)
	}
}

func (e *emitter) dropCrons(schedule []pipeprint.Schedule) {
	for _, s := range schedule {
		if s.Cron != "" {
			e.warnings.Add("triggers.schedule", "cron "+strconv.Quote(s.Cron)+" dropped; set schedules in GitLab")
		}
	}
}

// refPattern is a ref test, or "" when the glob cannot be translated.
type refPattern struct {
	glob, test string
}

// refs turns globs into ref tests on variable.
func refs(variable string, globs []string) []refPattern {
	patterns := make([]refPattern, 0, len(globs))
	for _, glob := range globs {
		patterns = append(patterns, refPattern{glob: glob, test: refTest(variable, glob)})
	}

	return patterns
}

// refRules returns one rule per ref test, or one rule for the source
// alone when there are none.
func (e *emitter) refRules(source string, paths []string, groups ...[]refPattern) []rule {
	var rules []rule
	for _, group := range groups {
		for _, p := range group {
			if p.test == "" {
				e.warnings.Add("triggers", "pattern "+strconv.Quote(p.glob)+" not translated; dropped")
				continue
			}

			rules = append(rules, rule{source: source, ref: p.test, changes: paths})
		}
	}

	if len(rules) == 0 {
		rules = append(rules, rule{source: source, changes: paths})
	}

	return rules
}

func rulesNode(rules []rule) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, r := range rules {
		cond := varSource + " == " + strconv.Quote(r.source)
		if r.ref != "" {
			cond += " && " + r.ref
		}

		m := yamlnode.NewMap()
		m.Add("if", yamlnode.Str(cond))
		if len(r.changes) > 0 {
			m.Add("changes", yamlnode.Seq(r.changes))
		}

		seq.Content = append(seq.Content, m.Node)
	}

	return seq
}

// refTest compares variable with glob: exactly, or by a regular
// expression for wildcards.
func refTest(variable, glob string) string {
	if !strings.ContainsAny(glob, "*?") {
		if strings.ContainsAny(glob, "[]!+") {
			return ""
		}

		return variable + " == " + strconv.Quote(glob)
	}

	re, ok := globRegex(glob)
	if !ok {
		return ""
	}

	return variable + " =~ /" + re + "/"
}

// parseRules reads workflow rules written by rules. It fails on any other
// rule.
func parseRules(n *yaml.Node) (pipeprint.Triggers, bool) {
	var t pipeprint.Triggers
	n = yamlnode.Resolve(n)
	if n == nil || n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		return t, false
	}

	for _, item := range n.Content {
		r, ok := readRule(item)
		if !ok || !addRule(&t, r) {
			return pipeprint.Triggers{}, false
		}
	}

	return t, true
}

func readRule(n *yaml.Node) (rule, bool) {
	pairs, ok := yamlnode.Pairs(n)
	if !ok {
		return rule{}, false
	}

	f := yamlnode.NewFields(pairs)
	cond := f.TakeString("if")
	changes := f.TakeStrings("changes")
	when := f.TakeString("when")
	if f.Rest() != nil || (when != "" && when != "always") {
		return rule{}, false
	}

	source, ref, _ := strings.Cut(cond, "&&")
	name, ok := cutTest(source, varSource, "==")
	if !ok {
		return rule{}, false
	}

	return rule{source: name, ref: strings.TrimSpace(ref), changes: changes}, true
}

// addRule merges r into t.
func addRule(t *pipeprint.Triggers, r rule) bool {
	switch r.source {
	case sourcePush:
		return addPush(t, r)
	case sourceMergeRequest:
		return addPullRequest(t, r)
	case sourceSchedule:
		t.Schedule = append(t.Schedule, pipeprint.Schedule{})
		return r.ref == "" && len(t.Schedule) == 1
	case sourceManual:
		t.Manual = &pipeprint.Manual{}
		return r.ref == ""
	}

	return false
}

func addPush(t *pipeprint.Triggers, r rule) bool {
	first := t.Push == nil
	if first {
		t.Push = &pipeprint.Push{Paths: r.changes}
	}

	if !first && !sameSet(t.Push.Paths, r.changes) {
		return false
	}

	if r.ref == "" {
		return first
	}

	if glob, ok := parseRefTest(r.ref, varBranch); ok {
		t.Push.Branches = append(t.Push.Branches, glob)
		return true
	}

	glob, ok := parseRefTest(r.ref, varTag)
	t.Push.Tags = append(t.Push.Tags, glob)

	return ok
}

func addPullRequest(t *pipeprint.Triggers, r rule) bool {
	first := t.PullRequest == nil
	if first {
		t.PullRequest = &pipeprint.PullRequest{Paths: r.changes}
	}

	if !first && !sameSet(t.PullRequest.Paths, r.changes) {
		return false
	}

	if r.ref == "" {
		return first
	}

	glob, ok := parseRefTest(r.ref, varTargetBranch)
	t.PullRequest.Branches = append(t.PullRequest.Branches, glob)

	return ok
}

// parseRefTest reads a test written by refTest and returns its glob.
func parseRefTest(test, variable string) (string, bool) {
	if value, ok := cutTest(test, variable, "=="); ok {
		return value, true
	}

	re, ok := strings.CutPrefix(strings.TrimSpace(test), variable+" =~ /")
	if !ok || !strings.HasSuffix(re, "/") {
		return "", false
	}

	return regexGlob(strings.TrimSuffix(re, "/"))
}

// cutTest reads `variable op "value"` and returns the value. Spacing may
// vary and the value may use single quotes.
func cutTest(test, variable, op string) (string, bool) {
	test = strings.Join(strings.Fields(test), " ")
	quoted, ok := strings.CutPrefix(test, variable+" "+op+" ")
	if !ok {
		return "", false
	}

	return unquote(quoted)
}

// unquote removes double or single quotes.
func unquote(s string) (string, bool) {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1], true
	}

	value, err := strconv.Unquote(s)

	return value, err == nil && strings.HasPrefix(s, `"`)
}
