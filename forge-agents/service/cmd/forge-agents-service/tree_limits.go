package main

import (
	"errors"
	"flag"
	"strconv"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-core/servicekit"
)

type treeFlags struct {
	maxDepth, maxChildren, maxTreeAgents, treeTimeout, maxTreeTokens string
}

func (f *treeFlags) register(fs *flag.FlagSet, env servicekit.Env) {
	fs.StringVar(&f.maxDepth, "max-depth", env("MAX_DEPTH", strconv.Itoa(agent.DefaultMaxDepth)), "maximum child depth")
	fs.StringVar(&f.maxChildren, "max-children", env("MAX_CHILDREN", strconv.Itoa(agent.DefaultMaxChildren)), "most direct children")
	fs.StringVar(&f.maxTreeAgents, "max-tree-agents", env("MAX_TREE_AGENTS", strconv.Itoa(agent.DefaultMaxTreeAgents)),
		"most agents in a tree")
	fs.StringVar(&f.treeTimeout, "tree-timeout", env("TREE_TIMEOUT", agent.DefaultTreeTimeout.String()),
		"lifetime of a tree from its first turn")
	fs.StringVar(&f.maxTreeTokens, "max-tree-tokens", env("MAX_TREE_TOKENS", strconv.Itoa(agent.DefaultMaxTreeTokens)),
		"most reported input and output tokens in a tree")
}

func (f treeFlags) apply(b *agent.Bounds) error {
	var err error
	if b.MaxDepth, err = strconv.Atoi(f.maxDepth); err != nil || b.MaxDepth <= 0 {
		return errors.New("invalid max-depth")
	}
	if b.MaxChildren, err = strconv.Atoi(f.maxChildren); err != nil || b.MaxChildren <= 0 {
		return errors.New("invalid max-children")
	}
	if b.MaxTreeAgents, err = strconv.Atoi(f.maxTreeAgents); err != nil || b.MaxTreeAgents <= 0 {
		return errors.New("invalid max-tree-agents")
	}
	if b.TreeTimeout, err = time.ParseDuration(f.treeTimeout); err != nil || b.TreeTimeout <= 0 {
		return errors.New("invalid tree-timeout")
	}
	if b.MaxTreeTokens, err = strconv.ParseInt(f.maxTreeTokens, 10, 64); err != nil || b.MaxTreeTokens <= 0 {
		return errors.New("invalid max-tree-tokens")
	}
	return nil
}
