//go:build windows
// +build windows

package main

import (
	"os/exec"
	"syscall"
)

func createAgentCommand(name string) *exec.Cmd {
	cmd := exec.Command(name)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Windows-specific fields:
		HideWindow: true,
		// No Setpgid field on Windows
	}
	return cmd
}
