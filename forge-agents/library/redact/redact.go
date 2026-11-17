// Package redact keeps secret values out of transcripts, raw output and
// logs.
package redact

import (
	"bytes"
	"slices"
	"strings"
)

// Mask replaces a secret value.
const Mask = "[redacted]"

// minLength is the shortest value treated as a secret; shorter values
// would match ordinary text.
const minLength = 8

// Redactor replaces known secret values.
type Redactor struct {
	secrets [][]byte
}

// secretName reports an environment variable name that holds a credential.
func secretName(name string) bool {
	upper := strings.ToUpper(name)
	for _, word := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "AUTH"} {
		if strings.Contains(upper, word) {
			return true
		}
	}
	return false
}

// FromEnv redacts the values of environment variables, given as
// NAME=value, whose names look like credentials, and the extra values.
func FromEnv(env []string, extra ...string) Redactor {
	values := slices.Clone(extra)
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if ok && secretName(name) {
			values = append(values, value)
		}
	}
	var r Redactor
	for _, v := range values {
		if len(v) >= minLength {
			r.secrets = append(r.secrets, []byte(v))
		}
	}
	// Longer secrets first, so one containing another is fully masked.
	slices.SortFunc(r.secrets, func(a, b []byte) int { return len(b) - len(a) })
	return r
}

// Bytes returns b with secrets masked; b is returned unchanged when it
// holds none.
func (r Redactor) Bytes(b []byte) []byte {
	for _, s := range r.secrets {
		if bytes.Contains(b, s) {
			b = bytes.ReplaceAll(b, s, []byte(Mask))
		}
	}
	return b
}

// String returns s with secrets masked.
func (r Redactor) String(s string) string {
	return string(r.Bytes([]byte(s)))
}
