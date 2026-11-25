//go:build windows

package duckdbagent

import "os/exec"

// The agent runs in a Linux container. This exists so the package builds everywhere.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Kill() }
}
