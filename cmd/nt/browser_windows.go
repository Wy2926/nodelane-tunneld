package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

func deviceBrowserCommand(ctx context.Context, target string) (*exec.Cmd, error) {
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, errDeviceBrowserUnavailable
	}
	command := exec.CommandContext(ctx, filepath.Join(systemDirectory, "rundll32.exe"), filepath.Join(systemDirectory, "url.dll")+",FileProtocolHandler", target)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return command, nil
}
