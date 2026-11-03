package code

import (
	"bytes"
	"errors"
	"io"

	"github.com/r59q/forge-tools/forge-core/forgeapi/linediff"
)

// side is one side's content for a patch.
type side struct {
	data []byte
	// full is false if the blob was over the read cap; data then holds
	// only its start.
	full bool
}

// patches fills the patch fields of changes in order, reading blobs by ID.
// Only the current change's content is held, and all reads together stop
// at patchInputBudget bytes.
func (t *treeDiff) patches(changes []Change, maxBlob int64) error {
	readCap := min(int64(patchInputFactor)*int64(t.opts.MaxPatchBytes), maxBlob)
	spent := int64(0) // blob bytes read
	read := func(id OID, mode FileMode) (side, error) {
		if id == "" {
			return side{full: true}, nil
		}
		if mode == ModeSubmodule {
			return side{data: []byte("Subproject commit " + string(id) + "\n"), full: true}, nil
		}

		s, err := t.readBlob(id, readCap)
		spent += int64(len(s.data))

		return s, err
	}
	done, out := 0, false // out: the call budget ran out
	for i := range changes {
		c := &changes[i]
		if c.OldID == c.ID {
			continue // a pure rename or mode change has no hunks
		}
		if out || done >= t.opts.MaxPatchFiles || spent >= patchInputBudget {
			c.PatchOmitted = true
			continue
		}

		done++
		a, err := read(c.OldID, c.OldMode)
		var b side
		if err == nil {
			b, err = read(c.ID, c.Mode)
		}

		switch {
		case exhausted(err):
			out, c.PatchOmitted = true, true
			continue
		case errors.Is(err, ErrTooLarge):
			c.PatchOmitted = true
			continue
		case err != nil:
			return err
		}

		if binary(a.data) || binary(b.data) {
			c.Binary = true
			continue
		}

		if !a.full || !b.full {
			c.PatchOmitted = true
			continue
		}

		patch, adds, dels := linediff.Hunks(a.data, b.data, t.opts.Context)
		c.Additions, c.Deletions = adds, dels
		if len(patch) > t.opts.MaxPatchBytes {
			c.PatchOmitted = true
			continue
		}

		c.Patch = patch
	}

	return nil
}

// readBlob reads blob id, at most readCap bytes, or its start if larger.
func (t *treeDiff) readBlob(id OID, readCap int64) (side, error) {
	rd, err := t.b.readBlob(t.o, id)
	if err != nil {
		return side{}, err
	}

	defer rd.Close()
	n := readCap + 1
	if rd.Size > readCap {
		n = binarySniff
	}

	data, err := io.ReadAll(io.LimitReader(rd, n))
	if err != nil {
		return side{}, err
	}
	if int64(len(data)) > readCap || rd.Size > readCap {
		// Copy the start so the readCap buffer is not kept.
		return side{data: bytes.Clone(data[:min(len(data), binarySniff)])}, nil
	}

	return side{data: data, full: true}, nil
}

// binary reports whether data has a NUL in its first 8000 bytes.
func binary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), binarySniff)], 0) >= 0
}
