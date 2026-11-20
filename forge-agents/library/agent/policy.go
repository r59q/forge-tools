package agent

import (
	"fmt"
	"slices"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
)

func (m *Manager) inherit(spec *Spec, parent Agent) error {
	if parent.ID != "" {
		if err := inheritChild(spec, parent); err != nil {
			return err
		}
	}
	if spec.Policy.Mode == "" {
		spec.Policy.Mode = adapter.ModeReadOnly
	}
	selected := Model{Client: spec.Client, Name: spec.Model}
	if len(spec.AllowedModels) == 0 {
		spec.AllowedModels = []Model{selected}
	}
	if !slices.Contains(spec.AllowedModels, selected) {
		return fmt.Errorf("%w: client and model are outside the policy", ErrInvalid)
	}
	for _, model := range spec.AllowedModels {
		if _, ok := m.cfg.Clients[model.Client]; !ok {
			return fmt.Errorf("%w: unknown allowed client %q", ErrInvalid, model.Client)
		}
		if err := adapter.CheckModel(model.Name); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	return nil
}

func inheritChild(spec *Spec, parent Agent) error {
	if spec.Client == "" {
		spec.Client = parent.Client
		if spec.Model == "" && !spec.modelSet {
			spec.Model = parent.Model
		}
	}
	if spec.Effort == "" && !spec.effortSet {
		spec.Effort = parent.Effort
	}
	if spec.Policy == (adapter.Policy{}) {
		spec.Policy = parent.Policy
	}
	if spec.Policy.Mode == "" {
		spec.Policy.Mode = parent.Policy.Mode
	}
	if permissionRank(spec.Policy.Mode) > permissionRank(parent.Policy.Mode) || spec.Policy.Ask && !parent.Policy.Ask {
		return fmt.Errorf("%w: child permissions exceed the parent's policy", ErrInvalid)
	}
	allowed := parent.AllowedModels
	if len(allowed) == 0 {
		allowed = []Model{{Client: parent.Client, Name: parent.Model}}
	}
	if len(spec.AllowedModels) == 0 {
		spec.AllowedModels = allowed
	}
	for _, model := range spec.AllowedModels {
		if !slices.Contains(allowed, model) {
			return fmt.Errorf("%w: child client/model allowlist exceeds the parent's policy", ErrInvalid)
		}
	}
	return nil
}

func permissionRank(mode string) int {
	switch mode {
	case adapter.ModeReadOnly:
		return 1
	case adapter.ModeEdit:
		return 2
	case adapter.ModeFull:
		return 3
	default:
		return 0 // The adapter rejects unknown modes.
	}
}
