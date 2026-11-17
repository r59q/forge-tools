//go:build !unix

package executor

import "os/exec"

// group keeps the default cancel, which kills only the process itself.
func group(*exec.Cmd) {}
