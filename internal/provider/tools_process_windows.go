package provider

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

func configureToolProcess(command *exec.Cmd) func() {
	command.Cancel = func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Terminate the shell's process tree, including a running test or pipeline.
		kill := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(command.Process.Pid), "/T", "/F")
		if err := kill.Run(); err != nil {
			return command.Process.Kill()
		}
		return nil
	}
	return func() {}
}
