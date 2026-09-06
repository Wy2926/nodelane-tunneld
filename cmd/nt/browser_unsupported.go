//go:build !linux && !windows

package main

import (
	"context"
	"os/exec"
)

func deviceBrowserCommand(context.Context, string) (*exec.Cmd, error) { return nil, nil }
