//go:build unix

package gitctx

import (
	"os/exec"
	"syscall"
)

// killGroup makes cmd the leader of its own process group and, when its
// context ends, kills the whole group: git's helpers (git-remote-https)
// must not outlive a timed-out fetch.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
