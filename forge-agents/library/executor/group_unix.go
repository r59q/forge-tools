//go:build unix

package executor

import (
	"os/exec"
	"syscall"
)

// group starts c in a new process group and kills the group on cancel.
func group(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}
