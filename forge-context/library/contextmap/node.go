// Package contextmap models repository locations, concepts, and their direct relationships.
package contextmap

import (
	"errors"
	"path"
	"strings"
)

// NodeKind identifies what a node represents.
type NodeKind string

const (
	// KindRepository is a repository and the root of its path hierarchy.
	KindRepository NodeKind = "repository"
	// KindFolder is a directory within a repository.
	KindFolder NodeKind = "folder"
	// KindFile is a file within a repository.
	KindFile NodeKind = "file"
	// KindLineRange is a span of lines within a file.
	KindLineRange NodeKind = "line-range"
	// KindConcept is an idea or entity with its own identity and no source location.
	KindConcept NodeKind = "concept"
)

// LineRange is a span of one-based line numbers, inclusive at both ends.
type LineRange struct {
	// Start is the first line.
	Start int
	// End is the last line. It can equal Start.
	End int
}

// NodeID is a comparable identity. Its fields depend on Kind.
type NodeID struct {
	// Kind is always required.
	Kind NodeKind
	// Repository is a caller-supplied key, required for every source location.
	Repository string
	// Path is a slash-separated, repository-relative path for a folder, file, or line range.
	Path string
	// Lines is required for a line range and zero for all other kinds.
	Lines LineRange
	// Concept is a caller-supplied key used only by concept nodes.
	Concept string
}

// Node is a source location or concept in the context map.
type Node struct {
	// ID identifies the node independently of its label.
	ID NodeID
	// Label is optional display text.
	Label string
}

func (id NodeID) validate() error {
	if id.Kind == KindConcept {
		if id.Concept == "" || id.Repository != "" || id.Path != "" || id.Lines != (LineRange{}) {
			return errors.New("concept nodes require a concept key and have no source location")
		}
		return nil
	}
	if id.Repository == "" || id.Concept != "" {
		return errors.New("source locations require a repository key and an empty concept key")
	}
	if id.Kind != KindLineRange && id.Lines != (LineRange{}) {
		return errors.New("only line-range nodes may specify lines")
	}
	return id.validateLocation()
}

func (id NodeID) validateLocation() error {
	switch id.Kind {
	case KindRepository:
		if id.Path != "" {
			return errors.New("repository nodes have an empty path")
		}
	case KindFolder, KindFile, KindLineRange:
		if !validPath(id.Path) {
			return errors.New("expected a clean repository-relative path")
		}
		if id.Kind == KindLineRange && (id.Lines.Start < 1 || id.Lines.End < id.Lines.Start) {
			return errors.New("expected a one-based line range with start at or before end")
		}
	default:
		return errors.New("unknown node kind")
	}
	return nil
}

func validPath(name string) bool {
	return name != "" && name != "." && name != ".." && !path.IsAbs(name) &&
		!strings.HasPrefix(name, "../") && !strings.ContainsRune(name, '\x00') && path.Clean(name) == name
}

func (id NodeID) parent() NodeID {
	switch id.Kind {
	case KindFolder, KindFile:
		dir := path.Dir(id.Path)
		if dir == "." {
			return NodeID{Kind: KindRepository, Repository: id.Repository}
		}
		return NodeID{Kind: KindFolder, Repository: id.Repository, Path: dir}
	case KindLineRange:
		return NodeID{Kind: KindFile, Repository: id.Repository, Path: id.Path}
	default:
		return NodeID{}
	}
}
