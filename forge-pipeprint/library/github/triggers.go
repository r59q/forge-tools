package github

import (
	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/portable"
	"github.com/r59q/forge-tools/forge-pipeprint/library/internal/yamlnode"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	"go.yaml.in/yaml/v3"
)

// Workflow event names.
const (
	eventPush        = "push"
	eventPullRequest = "pull_request"
	eventSchedule    = "schedule"
	eventManual      = "workflow_dispatch"
)

// triggerOrder is the order of events under on.
func triggerOrder() []string {
	return []string{eventPush, eventPullRequest, "*", eventSchedule, eventManual}
}

// parseTriggers reads on as an event name, a list of names or a mapping.
func parseTriggers(n *yaml.Node) (pipeprint.Triggers, bool) {
	var t pipeprint.Triggers
	if names, ok := yamlnode.AsStrings(n); ok {
		for _, name := range names {
			addEvent(&t, name, nil)
		}

		return t, true
	}

	pairs, ok := yamlnode.Pairs(n)
	if !ok {
		return t, false
	}

	for _, pair := range pairs {
		addEvent(&t, pair.Key, pair.Value)
	}

	return t, true
}

// addEvent models one event, or keeps it native when it cannot.
func addEvent(t *pipeprint.Triggers, name string, config *yaml.Node) {
	if schedule, ok := parseSchedule(config); ok && name == eventSchedule {
		t.Schedule = schedule
		return
	}

	f, err := fields(config, name)
	modeled := name == eventPush || name == eventPullRequest || name == eventManual
	if err != nil || !modeled {
		t.Native.Set(dialect, name, yamlnode.Plain(config))
		return
	}

	switch name {
	case eventPush:
		t.Push = &pipeprint.Push{
			Branches: f.TakeStrings("branches"),
			Tags:     f.TakeStrings("tags"),
			Paths:    f.TakeStrings("paths"),
		}
		portable.SetNative(&t.Push.Native, dialect, f.Rest())
	case eventPullRequest:
		t.PullRequest = &pipeprint.PullRequest{Branches: f.TakeStrings("branches"), Paths: f.TakeStrings("paths")}
		portable.SetNative(&t.PullRequest.Native, dialect, f.Rest())
	default:
		t.Manual = &pipeprint.Manual{}
		portable.SetNative(&t.Manual.Native, dialect, f.Rest())
	}
}

// parseSchedule reads a list of {cron} entries.
func parseSchedule(n *yaml.Node) ([]pipeprint.Schedule, bool) {
	n = yamlnode.Resolve(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil, false
	}

	var schedule []pipeprint.Schedule
	for _, item := range n.Content {
		pairs, _ := yamlnode.Pairs(item)
		cron, ok := yamlnode.AsString(yamlnode.Get(pairs, "cron"))
		if !ok || len(pairs) != 1 {
			return nil, false
		}

		schedule = append(schedule, pipeprint.Schedule{Cron: cron})
	}

	return schedule, true
}

// triggers writes on. Events without settings are written as a name or
// a list of names.
func (e *emitter) triggers(t pipeprint.Triggers) *yaml.Node {
	e.warnings.Foreign("triggers", t.Native, dialect)

	m := yamlnode.NewMap()
	if t.Push != nil {
		m.Add(eventPush, e.push(t.Push))
	}

	if t.PullRequest != nil {
		m.Add(eventPullRequest, e.pullRequest(t.PullRequest))
	}

	m.Add(eventSchedule, e.schedule(t.Schedule))
	if t.Manual != nil {
		e.warnings.Foreign("triggers.manual", t.Manual.Native, dialect)
		m.Add(eventManual, nativeOrNull(yamlnode.NewMap(), t.Manual.Native))
	}

	m.AddRest(t.Native[dialect])
	m.Sort(triggerOrder())

	return compactEvents(m, &e.warnings)
}

// compactEvents writes events without settings as names.
func compactEvents(m *yamlnode.Map, w *pipeprint.Warnings) *yaml.Node {
	if m.Len() == 0 {
		w.Add("triggers", "no triggers; defaulted to push")
		return yamlnode.Str(eventPush)
	}

	var names []string
	for i := 0; i < len(m.Node.Content); i += 2 {
		if !yamlnode.IsNull(m.Node.Content[i+1]) {
			return m.Node
		}

		names = append(names, m.Node.Content[i].Value)
	}

	return yamlnode.Strings(names)
}

func (e *emitter) push(push *pipeprint.Push) *yaml.Node {
	e.warnings.Foreign("triggers.push", push.Native, dialect)

	m := yamlnode.NewMap()
	addList(m, "branches", push.Branches)
	addList(m, "tags", push.Tags)
	addList(m, "paths", push.Paths)

	return nativeOrNull(m, push.Native)
}

func (e *emitter) pullRequest(pr *pipeprint.PullRequest) *yaml.Node {
	e.warnings.Foreign("triggers.pull_request", pr.Native, dialect)

	m := yamlnode.NewMap()
	addList(m, "branches", pr.Branches)
	addList(m, "paths", pr.Paths)

	return nativeOrNull(m, pr.Native)
}

func (e *emitter) schedule(schedule []pipeprint.Schedule) *yaml.Node {
	var entries []*yaml.Node
	for _, s := range schedule {
		if s.Cron == "" {
			e.warnings.Add("triggers.schedule", "schedule without cron dropped")
			continue
		}

		entry := yamlnode.NewMap()
		entry.Add("cron", yamlnode.Str(s.Cron))
		entries = append(entries, entry.Node)
	}

	if len(entries) == 0 {
		return nil
	}

	return &yaml.Node{Kind: yaml.SequenceNode, Content: entries}
}

// nativeOrNull adds n's github settings to m, and returns an empty value
// when m stays empty.
func nativeOrNull(m *yamlnode.Map, n pipeprint.Native) *yaml.Node {
	m.AddRest(n[dialect])
	if m.Len() == 0 {
		return yamlnode.Null()
	}

	return m.Node
}

// addList adds a nonempty list.
func addList(m *yamlnode.Map, key string, values []string) {
	if len(values) > 0 {
		m.Add(key, yamlnode.Seq(values))
	}
}
