package contextmap

import (
	"cmp"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
)

type heading struct {
	label string
	files []NodeID
}

// DiscoverConcepts builds a fresh graph from a full snapshot of file IDs and contents.
// It includes every file and rejects invalid or non-file IDs. Matching Markdown
// titles in at least two files produce a concept keyed by "heading:" plus the
// normalized title. Its closest discovered whole-word prefix is its parent.
func DiscoverConcepts(files map[NodeID]string) (*Graph, error) {
	var nodes []Node
	headings := make(map[string]*heading)
	for _, id := range slices.SortedFunc(maps.Keys(files), compareFiles) {
		if id.Kind != KindFile {
			return nil, fmt.Errorf("expected file evidence, got node kind %q", id.Kind)
		}
		nodes = append(nodes, Node{ID: id})
		title := markdownTitle(id.Path, files[id])
		if title == "" {
			continue
		}
		key := strings.ToLower(title)
		if headings[key] == nil {
			headings[key] = &heading{label: title}
		}
		headings[key].files = append(headings[key].files, id)
	}

	var links []Link
	for _, key := range slices.Sorted(maps.Keys(headings)) {
		h := headings[key]
		if len(h.files) < 2 {
			continue
		}
		concept := headingID(key)
		nodes = append(nodes, Node{ID: concept, Label: h.label})
		for _, file := range h.files {
			links = append(links, Link{Kind: LinkSupportedBy, From: concept, To: file})
		}
		if parent := parentHeading(key, headings); parent != "" {
			links = append(links, Link{Kind: LinkGroups, From: headingID(parent), To: concept})
		}
	}
	return NewGraph(nodes, links)
}

func compareFiles(a, b NodeID) int {
	return cmp.Or(cmp.Compare(a.Repository, b.Repository), cmp.Compare(a.Path, b.Path))
}

func markdownTitle(name, content string) string {
	if !strings.EqualFold(path.Ext(name), ".md") {
		return ""
	}
	for line := range strings.SplitSeq(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		title, ok := strings.CutPrefix(line, "# ")
		if ok {
			return strings.Join(strings.Fields(title), " ")
		}
		break
	}
	return ""
}

func headingID(key string) NodeID {
	return NodeID{Kind: KindConcept, Concept: "heading:" + key}
}

func parentHeading(key string, headings map[string]*heading) string {
	for {
		i := strings.LastIndexByte(key, ' ')
		if i < 0 {
			return ""
		}
		key = key[:i]
		if h := headings[key]; h != nil && len(h.files) >= 2 {
			return key
		}
	}
}
