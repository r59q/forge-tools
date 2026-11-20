package agent

import "context"

// recoverLinks fills the root and depth of records created before they were stored.
func (m *Manager) recoverLinks(ctx context.Context, agents []Agent) error {
	byID := make(map[string]*Agent, len(agents))
	for i := range agents {
		byID[agents[i].ID] = &agents[i]
	}
	var link func(*Agent) error
	link = func(a *Agent) error {
		if a.Root != "" {
			return nil
		}
		a.Root = a.ID
		if parent := byID[a.Parent]; parent != nil {
			if err := link(parent); err != nil {
				return err
			}
			a.Root, a.Depth = parent.Root, parent.Depth+1
		} else if a.Parent != "" {
			a.Root, a.Depth = a.Parent, 1
		}
		return m.cfg.Store.Put(ctx, *a)
	}
	for i := range agents {
		if err := link(&agents[i]); err != nil {
			return err
		}
	}
	return nil
}
