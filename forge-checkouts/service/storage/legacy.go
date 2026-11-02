package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/r59q/forge-tools/forge-checkouts/library/checkouts"
)

// loadLegacy reads the original schema only when checkouts.json is absent.
// Files and absolute locations stay in place; Update commits the new schema.
func (f *File) loadLegacy() (checkouts.State, error) {
	state := checkouts.State{Checkouts: map[string]checkouts.Record{}}
	b, err := os.ReadFile(filepath.Join(f.directory, "workspaces.json"))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return checkouts.State{}, err
	}
	var legacy struct {
		Version int `json:"version"`
		State   struct {
			Workspaces map[string]struct {
				Workspace    checkouts.Checkout `json:"workspace"`
				GitDirectory string             `json:"git_directory"`
			} `json:"workspaces"`
		} `json:"state"`
	}
	if err = json.Unmarshal(b, &legacy); err != nil {
		return checkouts.State{}, fmt.Errorf("storage: invalid legacy database: %w", err)
	}
	if legacy.Version != 1 || legacy.State.Workspaces == nil {
		return checkouts.State{}, errors.New("storage: invalid legacy database version or state")
	}
	for id, record := range legacy.State.Workspaces {
		state.Checkouts[id] = checkouts.Record{Checkout: record.Workspace, GitDirectory: record.GitDirectory}
	}
	return state, nil
}
