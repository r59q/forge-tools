package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"text/tabwriter"

	"github.com/r59q/forge-tools/forge-pipeprint/library/compile"
	"github.com/r59q/forge-tools/forge-pipeprint/library/graph"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	"github.com/r59q/forge-tools/forge-pipeprint/library/render"
)

// conversion holds the flags of import, export and convert.
type conversion struct {
	from, to, out string
	json, strict  bool
}

func (c *cli) importCmd(args []string) int {
	fs := c.flags("import")
	conv := conversion{to: string(compile.Pipeprint)}
	fs.StringVar(&conv.from, "from", "", "input format; detected when empty")
	fs.BoolVar(&conv.json, "json", false, "write Pipeprint JSON")
	c.outputFlags(fs, &conv)

	return c.convert(fs, args, &conv)
}

func (c *cli) exportCmd(args []string) int {
	fs := c.flags("export")
	conv := conversion{from: string(compile.Pipeprint)}
	fs.StringVar(&conv.to, "to", "", "output format (required)")
	c.outputFlags(fs, &conv)

	return c.convert(fs, args, &conv)
}

func (c *cli) convertCmd(args []string) int {
	fs := c.flags("convert")
	conv := conversion{}
	fs.StringVar(&conv.from, "from", "", "input format; detected when empty")
	fs.StringVar(&conv.to, "to", "", "output format (required)")
	c.outputFlags(fs, &conv)

	return c.convert(fs, args, &conv)
}

// outputFlags adds -o and --strict.
func (c *cli) outputFlags(fs *flag.FlagSet, conv *conversion) {
	fs.StringVar(&conv.out, "o", "", "output file; standard output when empty")
	fs.BoolVar(&conv.strict, "strict", false, "fail when settings are dropped")
}

func (c *cli) convert(fs *flag.FlagSet, args []string, conv *conversion) int {
	file, ok := c.parse(fs, args)
	if !ok {
		return exitUsage
	}

	if conv.json {
		conv.to = string(compile.PipeprintJSON)
	}

	if conv.to == "" {
		c.errorf("--to is required")
		return exitUsage
	}

	data, err := c.read(file)
	if err != nil {
		return c.fail(err)
	}

	out, warnings, err := compile.Convert(compile.Format(conv.from), compile.Format(conv.to), data)
	if err != nil {
		return c.fail(err)
	}

	c.warn(warnings)
	if conv.strict && len(warnings) > 0 {
		c.errorf("%d settings dropped; output not written", len(warnings))
		return exitFail
	}

	if err := c.write(conv.out, out); err != nil {
		return c.fail(err)
	}

	return exitOK
}

func (c *cli) warn(warnings pipeprint.Warnings) {
	for _, w := range warnings {
		c.errorf("warning: %s", w)
	}
}

func (c *cli) checkCmd(args []string) int {
	fs := c.flags("check")
	from := fs.String("from", "", "input format; detected when empty")
	file, ok := c.parse(fs, args)
	if !ok {
		return exitUsage
	}

	data, err := c.read(file)
	if err != nil {
		return c.fail(err)
	}

	format, problems := compile.Check(compile.Format(*from), data)
	for _, p := range problems {
		c.errorf("%s", p)
	}

	if len(problems) > 0 {
		return exitFail
	}

	c.print(c.stdout, fmt.Sprintf("ok: valid %s pipeline\n", format))

	return exitOK
}

func (c *cli) graphCmd(args []string) int {
	fs := c.flags("graph")
	from := fs.String("from", "", "input format; detected when empty")
	asJSON := fs.Bool("json", false, "write nodes and edges as JSON")
	var o render.Options
	fs.BoolVar(&o.Steps, "steps", false, "list each job's steps")
	fs.BoolVar(&o.ASCII, "ascii", false, "draw with ASCII only")
	fs.IntVar(&o.Width, "width", 0, "wrap rows of jobs at this width (default 100)")

	file, ok := c.parse(fs, args)
	if !ok {
		return exitUsage
	}

	data, err := c.read(file)
	if err != nil {
		return c.fail(err)
	}

	p, _, err := compile.Read(compile.Format(*from), data)
	if err != nil {
		return c.fail(err)
	}

	if !*asJSON {
		c.print(c.stdout, render.Text(p, o))
		return exitOK
	}

	out, err := json.MarshalIndent(graph.Build(p), "", "  ")
	if err != nil {
		return c.fail(err)
	}

	c.print(c.stdout, string(out)+"\n")

	return exitOK
}

func (c *cli) formatsCmd(args []string) int {
	if len(args) > 0 {
		c.errorf("formats takes no arguments")
		return exitUsage
	}

	w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	for _, info := range compile.Formats() {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", info.Format, info.Title, info.Path)
	}

	if err := w.Flush(); err != nil {
		return c.fail(err)
	}

	return exitOK
}
