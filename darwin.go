//go:build darwin

package main

import (
	"os/exec"
	"syscall"
)

func createAgentCommand(name string) *exec.Cmd {
	cmd := exec.Command(name)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Darwin-specific settings if needed
	}
	return cmd
}
