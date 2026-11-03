package code

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitwire"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// push sends cmds and a pack of objs to the receive-pack endpoint at url.
// It checks every Old against the advertisement first, sends nothing on
// a mismatch, and classifies failures as ErrStale, ErrRejected or
// ErrUnsupported.
func push(ctx context.Context, git *forgeauth.GitRemote, url string, cmds []gitwire.Command, objs []gitobj.Object) error {
	adv, err := gitwire.Advertise(ctx, git.Client, url)
	if err != nil {
		return gitError(err)
	}

	for _, c := range cmds {
		if cur := adv.Refs[c.Name]; cur != c.Old {
			return fmt.Errorf("%w: %s is at %s, want %s", ErrStale, c.Name, refValue(cur), refValue(c.Old))
		}
	}

	caps, err := pushCaps(adv.Caps, cmds)
	if err != nil {
		return err
	}

	pack, err := packReader(objs)
	if err != nil {
		return err
	}

	rep, err := gitwire.Push(ctx, git.Client, url, cmds, caps, pack)
	if err != nil {
		return recoverPush(ctx, git, url, cmds, err)
	}

	reasons, failed := pushFailures(rep, cmds)
	if len(reasons) == 0 {
		return nil
	}

	return pushRefused(ctx, git, url, rep, failed, strings.Join(reasons, "; "))
}

// pushCaps returns the capabilities to ask for when pushing cmds to a
// server with caps. Several commands need atomic and deletes delete-refs.
func pushCaps(caps gitwire.Capabilities, cmds []gitwire.Command) ([]string, error) {
	out := []string{"report-status"}
	if len(cmds) > 1 {
		if !caps.Has("atomic") {
			return nil, fmt.Errorf("%w: the forge does not support atomic pushes of several refs", ErrUnsupported)
		}

		out = append(out, "atomic")
	}

	if slices.ContainsFunc(cmds, func(c gitwire.Command) bool { return c.New.IsZero() }) {
		if !caps.Has("delete-refs") {
			return nil, fmt.Errorf("%w: the forge does not allow deleting refs by push", ErrUnsupported)
		}

		out = append(out, "delete-refs")
	}

	for _, opt := range []string{"side-band-64k", "quiet"} {
		if caps.Has(opt) {
			out = append(out, opt)
		}
	}

	if caps.Has("object-format") {
		out = append(out, "object-format=sha1")
	}

	return out, nil
}

// packReader returns a pack of objs, or nil if there are none.
func packReader(objs []gitobj.Object) (io.Reader, error) {
	if len(objs) == 0 {
		return nil, nil
	}

	var buf bytes.Buffer
	if err := gitwire.WritePack(&buf, objs); err != nil {
		return nil, err
	}

	return &buf, nil
}

// pushFailures returns why rep failed, if it did, and the commands that
// failed; all of them if only the unpack failed.
func pushFailures(rep *gitwire.Report, cmds []gitwire.Command) ([]string, []gitwire.Command) {
	var reasons []string
	if !rep.UnpackOK {
		reasons = append(reasons, "unpack: "+rep.Unpack)
	}

	var failed []gitwire.Command
	for _, c := range cmds {
		st, ok := rep.Refs[c.Name]
		switch {
		case !ok:
			reasons = append(reasons, c.Name+": no status")
		case !st.OK:
			reasons = append(reasons, c.Name+": "+st.Reason)
		default:
			continue
		}

		failed = append(failed, c)
	}

	if len(failed) == 0 {
		failed = cmds
	}

	return reasons, failed
}

// pushRefused tells a lost race from a refusal of failed by looking at
// the refs again. A ref at New looks applied, as OIDs are deterministic.
// When in doubt it is a refusal, never a false ErrStale.
func pushRefused(
	ctx context.Context,
	git *forgeauth.GitRemote,
	url string,
	rep *gitwire.Report,
	failed []gitwire.Command,
	why string,
) error {
	now, err := gitwire.Advertise(ctx, git.Client, url)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrRejected, why)
	}

	atNew, moved := true, ""
	for _, c := range failed {
		cur := now.Refs[c.Name]
		if cur == c.New {
			continue
		}

		atNew = false
		if cur != c.Old && moved == "" {
			moved = c.Name
		}
	}

	switch {
	case moved != "":
		return fmt.Errorf("%w: %s moved during the push (%s)", ErrStale, moved, why)
	case atNew && rep.UnpackOK:
		return nil
	}

	return fmt.Errorf("%w: %s", ErrRejected, why)
}

// recoverPush finds out what a push that failed in transit did. If every
// ref is at its New value the push applied. Otherwise the outcome is
// unknown and it returns err: a ref still at Old may yet move if the
// forge applies the push late, and a ref elsewhere may hold our push with
// another on top, so it is never ErrStale.
func recoverPush(ctx context.Context, git *forgeauth.GitRemote, url string, cmds []gitwire.Command, err error) error {
	var fe *forgehttp.Error
	if ctx.Err() != nil || (errors.As(err, &fe) && fe.StatusCode < http.StatusInternalServerError) {
		return gitError(err)
	}

	now, aerr := gitwire.Advertise(ctx, git.Client, url)
	if aerr != nil {
		return fmt.Errorf("code: push outcome unknown: %w", err)
	}

	atNew, atOld := true, true
	for _, c := range cmds {
		cur := now.Refs[c.Name]
		atNew = atNew && cur == c.New
		atOld = atOld && cur == c.Old
	}

	switch {
	case atNew:
		return nil
	case atOld:
		return fmt.Errorf("code: push outcome unknown, refs unchanged: %w", err)
	}

	return fmt.Errorf("code: push outcome unknown, refs moved: %w", err)
}

// gitError maps gitwire sentinels to the package's errors.
func gitError(err error) error {
	switch {
	case errors.Is(err, gitwire.ErrUnsupported):
		return fmt.Errorf("%w: %w", ErrUnsupported, err)
	case errors.Is(err, gitwire.ErrTooLarge):
		return fmt.Errorf("%w: %w", ErrTooLarge, err)
	}

	return err
}

// refValue describes a ref value in errors.
func refValue(id gitobj.ID) string {
	if id.IsZero() {
		return "absent"
	}

	return id.String()
}
