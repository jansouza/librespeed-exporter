//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
)

// setGracefulCancel makes cancellation send SIGTERM first. If the process does
// not exit within cmd.WaitDelay, exec kills it.
func setGracefulCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
}
