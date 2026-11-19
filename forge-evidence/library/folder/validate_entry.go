package folder

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

type entryCheck struct {
	walk         *validation
	dir          dir
	listed       map[string]evidence.File
	toVerify     []evidence.File
	bytesLeft    int64
	fileTooLarge bool
}

func (w *validation) checkEntry(ctx context.Context, d dir, id evidence.ID) error {
	entry, metadataLimit, err := w.metadata(d, id)
	if err != nil {
		return err
	}
	check := entryCheck{walk: w, dir: d, bytesLeft: w.limits.EntryBytes}
	if len(entry.Files) > 0 {
		check.listed = make(map[string]evidence.File, len(entry.Files))
		for _, file := range entry.Files {
			check.listed[file.Name] = file
		}
	}

	// Give the metadata its own slot. listChildren also reads one lookahead.
	children, err := listChildren(d, min(w.limits.Files, math.MaxInt-1)+1)
	if errors.Is(err, evidence.ErrTooLarge) || len(children)-metadataCount(children) > w.limits.Files {
		check.fileCount(metadataLimit)
		return nil
	}
	if err != nil {
		return err
	}

	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return err
		}
		if child.Name() == evidence.MetadataFile {
			continue
		}
		if err := check.file(child.Name()); err != nil {
			return err
		}
	}
	for _, file := range entry.Files {
		if _, missing := check.listed[file.Name]; missing {
			w.add(d.join(file.Name), storage.MissingFile, "listed file is missing")
		}
	}
	check.sizeLimits(metadataLimit)
	return check.verify(ctx)
}

func metadataCount(children []fs.DirEntry) int {
	for _, child := range children {
		if child.Name() == evidence.MetadataFile {
			return 1
		}
	}
	return 0
}

// metadata reports structural problems and returns declared size or count
// violations for comparison with the actual inventory.
func (w *validation) metadata(d dir, id evidence.ID) (evidence.Entry, storage.Reason, error) {
	path := d.join(evidence.MetadataFile)
	info, err := d.root.Lstat(evidence.MetadataFile)
	if errors.Is(err, fs.ErrNotExist) {
		w.add(path, storage.MissingMetadata, "metadata file is missing")
		return evidence.Entry{}, "", nil
	}
	if err != nil {
		return evidence.Entry{}, "", pathError(path, err)
	}
	if !w.node(path, info.Mode(), false) {
		return evidence.Entry{}, "", nil
	}

	entry, err := loadEntry(d, id, w.limits)
	switch {
	case err == nil:
		return entry, "", nil
	case errors.Is(err, evidence.ErrFileTooLarge):
		return evidence.Entry{}, storage.FileTooLarge, nil
	case errors.Is(err, evidence.ErrTooManyFiles):
		return evidence.Entry{}, storage.TooManyFiles, nil
	case errors.Is(err, evidence.ErrEntryTooLarge):
		return evidence.Entry{}, storage.EntryTooLarge, nil
	case errors.Is(err, evidence.ErrTooLarge):
		w.add(path, storage.OversizedMetadata, "metadata exceeds the byte limit")
	case errors.Is(err, evidence.ErrVersion):
		w.add(path, storage.UnknownVersion, "metadata format version is unsupported")
	case errors.Is(err, evidence.ErrMalformed):
		w.add(path, storage.MalformedMetadata, err.Error())
	case errors.Is(err, evidence.ErrInvalid):
		w.add(path, storage.InvalidField, err.Error())
	default:
		return evidence.Entry{}, "", err
	}
	return evidence.Entry{}, "", nil
}

func (c *entryCheck) file(name string) error {
	path := c.dir.join(name)
	file, listed := c.listed[name]
	delete(c.listed, name)

	info, err := c.dir.root.Lstat(name)
	if err != nil {
		return pathError(path, err)
	}
	if !c.walk.node(path, info.Mode(), false) {
		return nil
	}
	c.bytesLeft = max(c.bytesLeft-info.Size(), -1)

	if !c.walk.name(path, evidence.ValidFileName(name)) {
		return nil
	}
	switch {
	case info.Size() > c.walk.limits.FileBytes:
		c.fileTooLarge = true
		c.walk.add(path, storage.FileTooLarge, "file exceeds the byte limit")
	case c.listed == nil:
		// A broken metadata file cannot tell us which files were expected.
	case !listed:
		c.walk.add(path, storage.UnlistedFile, "file is absent from the metadata")
	case info.Size() != file.Size:
		c.walk.add(path, storage.FileMismatch, "file size differs from the metadata")
	default:
		c.toVerify = append(c.toVerify, file)
	}
	return nil
}

func (c *entryCheck) fileCount(metadataLimit storage.Reason) {
	c.walk.add(c.dir.path, storage.TooManyFiles, "entry exceeds the file count limit")
	if metadataLimit != storage.TooManyFiles {
		c.sizeLimits(metadataLimit)
	}
}

func (c *entryCheck) sizeLimits(metadataLimit storage.Reason) {
	path := c.dir.path
	switch {
	case metadataLimit == storage.TooManyFiles:
		c.walk.add(path, storage.TooManyFiles, "metadata lists too many files")
	case metadataLimit == storage.FileTooLarge && !c.fileTooLarge:
		c.walk.add(path, storage.FileTooLarge, "metadata records a file over the byte limit")
	}
	if c.bytesLeft < 0 || metadataLimit == storage.EntryTooLarge {
		c.walk.add(path, storage.EntryTooLarge, "entry exceeds the total file byte limit")
	}
}

func (c *entryCheck) verify(ctx context.Context) error {
	if c.bytesLeft < 0 {
		return nil
	}
	for _, file := range c.toVerify {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := c.dir.join(file.Name)
		content, err := readFile(c.dir, file.Name, file.Size)
		if err != nil {
			// The inventory already checked type and size. A change here
			// makes the validation incomplete.
			return fmt.Errorf("%q: %w", path, storage.ErrUnreadable)
		}
		if !matches(file, content) {
			c.walk.add(path, storage.FileMismatch, "file SHA-256 differs from the metadata")
		}
	}
	return nil
}
