//go:build !unix && !windows

package provider

import "os/exec"

func configureToolProcess(command *exec.Cmd) func() {
	// CommandContext terminates the shell; WaitDelay bounds inherited pipes.
	return func() {}
}
