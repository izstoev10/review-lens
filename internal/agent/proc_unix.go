//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
)

// isolateForCancel puts the agent in its own process group and makes context
// cancellation kill that whole group.
//
// Killing only the direct child isn't enough: the agent CLI spawns children of
// its own (shells, search tools, editors), and they inherit its stdout pipe.
// Those grandchildren would keep the pipe open — so our reader blocks until
// they finish anyway — and, worse, keep editing files after we've told the
// caller the agent has stopped.
//
// Applied only to the cancellable entry points. A plain, uncancellable run
// stays in the caller's process group so a terminal Ctrl+C still reaches it.
func isolateForCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid: signal the whole process group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
