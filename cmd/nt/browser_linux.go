package main

import (
	"context"
	"os"
	"os/exec"
)

func deviceBrowserCommand(ctx context.Context, target string) (*exec.Cmd, error) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, nil
	}
	return exec.CommandContext(ctx, "xdg-open", target), nil
}
