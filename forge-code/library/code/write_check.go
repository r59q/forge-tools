package code

import (
	"fmt"
	"strings"
)

// checkCommit checks a NewCommit's arguments.
func (c *Client) checkCommit(n NewCommit) error {
	if err := checkRefName(n.Ref); err != nil {
		return err
	}
	if err := checkOID("old", n.Old); err != nil {
		return err
	}
	if len(n.Parents) > maxParents {
		return fmt.Errorf("%w: more than %d parents", ErrInvalid, maxParents)
	}

	seen := map[OID]bool{}
	for _, p := range n.Parents {
		if err := checkOID("parent", p); err != nil {
			return err
		}
		if p.zero() || seen[p] {
			return fmt.Errorf("%w: parent %s zero or repeated", ErrInvalid, p)
		}

		seen[p] = true
	}

	if strings.TrimSpace(n.Message) == "" || strings.ContainsRune(n.Message, 0) {
		return fmt.Errorf("%w: empty commit message or NUL in it", ErrInvalid)
	}
	if err := checkSignature("author", n.Author); err != nil {
		return err
	}
	if n.Committer != (Signature{}) {
		if err := checkSignature("committer", n.Committer); err != nil {
			return err
		}
	}

	return checkChanges(n.Changes, len(n.Parents) > 0, c.limits.MaxWriteBytes)
}

// checkSignature checks a name and email that go into a commit header.
func checkSignature(what string, s Signature) error {
	for _, v := range []string{s.Name, s.Email} {
		if strings.TrimSpace(v) == "" || strings.ContainsAny(v, "<>\n\r\x00") {
			return fmt.Errorf("%w: %s name and email must be set, without <, > or newlines", ErrInvalid, what)
		}
	}

	return nil
}

// checkChanges checks file changes: valid paths, one change per path, no
// file inside another new file, sane modes, and content within max bytes.
// Without a parent there is nothing to delete or keep a mode of.
func checkChanges(changes []FileChange, hasParent bool, max int64) error {
	seen := map[string]bool{}  // every changed path
	added := map[string]bool{} // paths that are not deleted
	var total int64
	for _, ch := range changes {
		if err := checkWritePath(ch.Path); err != nil {
			return err
		}
		if seen[ch.Path] {
			return badChange(ch, "repeated path")
		}

		seen[ch.Path] = true
		if err := checkChange(ch, hasParent); err != nil {
			return err
		}
		if ch.Delete {
			continue
		}

		added[ch.Path] = true
		total += int64(len(ch.Content))
		if total > max {
			return fmt.Errorf("%w: commit content over %d bytes", ErrTooLarge, max)
		}
	}

	return checkNesting(added)
}

// checkChange checks the fields of one change against its kind.
func checkChange(ch FileChange, hasParent bool) error {
	switch {
	case ch.Delete:
		if ch.Mode != 0 || ch.Content != nil || ch.Submodule != "" {
			return badChange(ch, "delete with mode, content or submodule")
		}
		if !hasParent {
			return badChange(ch, "delete in a root commit")
		}
	case ch.Mode == ModeSubmodule:
		if ch.Content != nil {
			return badChange(ch, "submodule with content")
		}

		return checkOID("submodule", ch.Submodule)
	case ch.Submodule != "":
		return badChange(ch, "submodule without ModeSubmodule")
	case ch.Mode == ModeTree || (ch.Mode != 0 && !ch.Mode.valid()):
		return badChange(ch, "mode "+ch.Mode.String())
	}

	return nil
}

// badChange returns an ErrInvalid error about ch.
func badChange(ch FileChange, why string) error {
	return fmt.Errorf("%w: change to %q: %s", ErrInvalid, ch.Path, why)
}

// checkNesting checks that no added path sits below another.
func checkNesting(added map[string]bool) error {
	for p := range added {
		for dir := p; ; {
			i := strings.LastIndexByte(dir, '/')
			if i < 0 {
				break
			}
			if dir = dir[:i]; added[dir] {
				return fmt.Errorf("%w: change to %q is below the file %q", ErrInvalid, p, dir)
			}
		}
	}

	return nil
}

// checkRefUpdates checks UpdateRefs arguments: at least one update, valid
// and distinct names, and full object IDs that differ.
func checkRefUpdates(u []RefUpdate) error {
	if len(u) == 0 {
		return fmt.Errorf("%w: no ref updates", ErrInvalid)
	}

	seen := map[string]bool{}
	for _, up := range u {
		if err := checkRefName(up.Name); err != nil {
			return err
		}
		if seen[up.Name] {
			return fmt.Errorf("%w: ref %q updated twice", ErrInvalid, up.Name)
		}

		seen[up.Name] = true
		if err := checkOID("old", up.Old); err != nil {
			return err
		}
		if err := checkOID("new", up.New); err != nil {
			return err
		}
		if up.Old == up.New || (up.Old.zero() && up.New.zero()) {
			return fmt.Errorf("%w: ref %q: old and new are the same", ErrInvalid, up.Name)
		}
	}

	return nil
}
