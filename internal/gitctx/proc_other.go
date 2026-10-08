//go:build !unix

package gitctx

import "os/exec"

// killGroup keeps exec's default on systems without process groups: the
// git process is killed and WaitDelay bounds the wait for its helpers.
func killGroup(*exec.Cmd) {}
