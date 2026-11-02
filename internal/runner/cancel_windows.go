//go:build windows

package runner

import "os/exec"

// setGracefulCancel keeps the exec default on Windows, which has no SIGTERM:
// cancellation kills the process.
func setGracefulCancel(*exec.Cmd) {}
