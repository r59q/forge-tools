package issues

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// withLabels keeps the issues that have every label, ignoring case. Forges
// filter too, but not always for all labels, such as Forgejo's search.
func withLabels(list []Issue, labels []string) []Issue {
	if len(labels) == 0 {
		return list
	}

	kept := list[:0]
	for _, iss := range list {
		if hasAll(iss.Labels, labels) {
			kept = append(kept, iss)
		}
	}

	return kept
}

// hasAll reports whether have contains every name in want, ignoring case.
func hasAll(have, want []string) bool {
	for _, w := range want {
		ok := false
		for _, h := range have {
			if strings.EqualFold(h, w) {
				ok = true
				break
			}
		}

		if !ok {
			return false
		}
	}

	return true
}

// checkList checks opts and returns its forge filter.
func checkList(opts ListOptions) (filter, error) {
	f := filter{state: opts.State, labels: opts.Labels}
	switch f.state {
	case "":
		f.state = StateOpen
	case StateOpen, StateClosed, StateAll:
	default:
		return filter{}, fmt.Errorf("%w: state %q, want open, closed or all", ErrInvalid, opts.State)
	}

	switch opts.Scope {
	case "", Assigned, Created:
	default:
		return filter{}, fmt.Errorf("%w: scope %q, want assigned or created", ErrInvalid, opts.Scope)
	}

	if opts.Repository != "" {
		if opts.Scope != "" {
			return filter{}, fmt.Errorf("%w: scope applies only without a repository", ErrInvalid)
		}
		if _, ok := forgerest.CleanPath(opts.Repository); !ok {
			return filter{}, invalidPath(opts.Repository)
		}
	}

	return f, checkNames("label", opts.Labels)
}

// checkNames checks label names or logins. Forges join them with commas,
// so a comma would split one name in two.
func checkNames(what string, names []string) error {
	for _, n := range names {
		if strings.TrimSpace(n) == "" || strings.Contains(n, ",") {
			return fmt.Errorf("%w: %s %q", ErrInvalid, what, n)
		}
	}

	return nil
}

// gone wraps a 410 Gone, which GitHub sends for a deleted issue or when
// issues are disabled, with forgehttp.ErrNotFound.
func gone(err error) error {
	var fe *forgehttp.Error
	if errors.As(err, &fe) && fe.StatusCode == http.StatusGone {
		return fmt.Errorf("%w: %w", forgehttp.ErrNotFound, err)
	}

	return err
}

// notFound is the error for a repository path that cannot exist on s.
func notFound(s *source, path string) error {
	return fmt.Errorf("issues: source %s: %q: %w", s.ID, path, forgehttp.ErrNotFound)
}

// invalidPath is the error for a path that forgerest.CleanPath rejects.
func invalidPath(path string) error {
	return fmt.Errorf("%w: repository %q, want owner/name", ErrInvalid, path)
}
