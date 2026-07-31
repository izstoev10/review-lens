//go:build windows

package agent

import "os/exec"

// isolateForCancel is a no-op on Windows, which has no POSIX process groups.
// Cancellation falls back to os/exec's default: killing the agent process
// itself. See the Unix build of this file for what that misses.
func isolateForCancel(cmd *exec.Cmd) {}
