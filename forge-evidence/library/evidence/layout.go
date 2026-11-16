package evidence

import (
	"fmt"
	"strings"
)

// Root is the evidence folder at the repository root.
const Root = ".evidence"

// MetadataFile is the name of the metadata file in each entry folder.
const MetadataFile = "entry.json"

// ID identifies an entry: the commit it is about and its name. Each ID has
// one folder, Root/<commit>/<name>.
type ID struct {
	// Commit is the subject commit, as ValidCommit requires.
	Commit string

	// Name is the entry name, as ValidName requires.
	Name string
}

// Validate reports whether the ID is a valid commit and name. Otherwise it
// returns ErrInvalid.
func (id ID) Validate() error {
	if err := ValidCommit(id.Commit); err != nil {
		return err
	}
	if err := ValidName(id.Name); err != nil {
		return fmt.Errorf("entry %w", err)
	}
	return nil
}

// CommitDir returns the folder of the entries about a commit,
// Root/<commit>, or ErrInvalid for an invalid commit.
func CommitDir(commit string) (string, error) {
	if err := ValidCommit(commit); err != nil {
		return "", err
	}
	return Root + "/" + commit, nil
}

// Dir returns the entry folder, Root/<commit>/<name>, relative to the
// repository root with '/' separators, or ErrInvalid for an invalid ID.
func (id ID) Dir() (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	return Root + "/" + id.Commit + "/" + id.Name, nil
}

// MetadataPath returns the path of the entry's metadata file,
// Root/<commit>/<name>/MetadataFile.
func (id ID) MetadataPath() (string, error) {
	return id.FilePath(MetadataFile)
}

// FilePath returns the path of a file in the entry folder,
// Root/<commit>/<name>/<file>, or ErrInvalid for an invalid ID or file name.
func (id ID) FilePath(file string) (string, error) {
	dir, err := id.Dir()
	if err != nil {
		return "", err
	}
	if err := ValidName(file); err != nil {
		return "", fmt.Errorf("file %w", err)
	}
	return dir + "/" + file, nil
}

// ParsePath maps a path from Dir or FilePath back to the entry ID and file
// name. The file name is empty for an entry folder. Any other path returns
// ErrInvalid, without the path.
func ParsePath(path string) (ID, string, error) {
	elements := strings.Split(path, "/")

	isEntryPath := len(elements) == 3 || len(elements) == 4
	if !isEntryPath || elements[0] != Root {
		return ID{}, "", invalid("path", "is not an entry folder or file")
	}

	id := ID{Commit: elements[1], Name: elements[2]}
	if err := id.Validate(); err != nil {
		return ID{}, "", err
	}
	if len(elements) == 3 {
		return id, "", nil
	}

	file := elements[3]
	if err := ValidName(file); err != nil {
		return ID{}, "", fmt.Errorf("file %w", err)
	}
	return id, file, nil
}
