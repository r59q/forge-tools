// Package render draws pipelines as text: jobs in boxes, grouped by needs
// level from top to bottom.
package render

import (
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-pipeprint/library/graph"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
)

// defaultWidth is the line width when Options.Width is zero.
const defaultWidth = 100

// Options change the drawing.
type Options struct {
	// Steps lists each job's steps.
	Steps bool
	// ASCII draws with ASCII characters only.
	ASCII bool
	// Width wraps rows of boxes; zero means 100.
	Width int
}

// Text draws p. p should be valid.
func Text(p *pipeprint.Pipeline, o Options) string {
	c := unicodeChars()
	if o.ASCII {
		c = asciiChars()
	}

	limit := o.Width
	if limit <= 0 {
		limit = defaultWidth
	}

	g := graph.Build(p)
	lines := header(p)
	for l := 1; l <= g.Levels; l++ {
		if l > 1 {
			lines = append(lines, "  "+c.vertical, "  "+c.arrow)
		}

		nodes := g.Level(l)
		lines = append(lines, levelTitle(l, nodes, c))

		boxes := make([][]string, len(nodes))
		for i, n := range nodes {
			boxes[i] = box(content(g, n, o, c), c)
		}

		lines = append(lines, row(boxes, limit)...)
	}

	return strings.Join(lines, "\n") + "\n"
}

func header(p *pipeprint.Pipeline) []string {
	name := p.Name
	if name == "" {
		name = "pipeline"
	}

	lines := []string{name}
	if on := Triggers(p.Triggers); on != "" {
		lines = append(lines, "on: "+on)
	}

	return append(lines, "")
}

// levelTitle numbers a level and names its stage when all jobs share one.
func levelTitle(level int, nodes []graph.Node, c charset) string {
	title := "level " + strconv.Itoa(level)
	stage := nodes[0].Stage
	for _, n := range nodes {
		if n.Stage != stage {
			return title
		}
	}

	if stage != "" {
		title += " " + c.separator + " " + stage
	}

	return title
}

// content returns a job box's lines.
func content(g graph.Graph, n graph.Node, o Options, c charset) []string {
	title := n.Label()
	if n.Matrix {
		title += " (matrix)"
	}

	lines := []string{title}
	if needs := g.Needs(n.ID); len(needs) > 0 {
		lines = append(lines, c.needs+" "+strings.Join(needs, ", "))
	}

	if n.If != "" {
		lines = append(lines, "if "+n.If)
	}

	if where := runsIn(n); where != "" {
		lines = append(lines, where)
	}

	if o.Steps {
		for _, step := range n.Steps {
			lines = append(lines, c.step+" "+step)
		}
	}

	return lines
}

// runsIn names the image, or the runner labels.
func runsIn(n graph.Node) string {
	if n.Image != "" {
		return "image " + n.Image
	}

	if len(n.RunsOn) > 0 {
		return "runs on " + strings.Join(n.RunsOn, ", ")
	}

	return ""
}

// Triggers summarizes triggers, such as "push [main], pull_request".
func Triggers(t pipeprint.Triggers) string {
	var parts []string
	if t.Push != nil {
		parts = append(parts, filtered("push", append(t.Push.Branches, t.Push.Tags...)))
	}

	if t.PullRequest != nil {
		parts = append(parts, filtered("pull_request", t.PullRequest.Branches))
	}

	for _, s := range t.Schedule {
		parts = append(parts, filtered("schedule", nonEmpty(s.Cron)))
	}

	if t.Manual != nil {
		parts = append(parts, "manual")
	}

	for _, d := range pipeprint.Dialects() {
		parts = append(parts, pipeprint.SortedKeys(t.Native[d])...)
	}

	return strings.Join(parts, ", ")
}

func filtered(event string, filters []string) string {
	if len(filters) == 0 {
		return event
	}

	return event + " [" + strings.Join(filters, ", ") + "]"
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}

	return []string{s}
}
