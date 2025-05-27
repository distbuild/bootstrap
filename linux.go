//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

func createAgentCommand(name string) *exec.Cmd {
	cmd := exec.Command(name)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Unix-specific fields:
		Setpgid: true,
	}
	return cmd
}
