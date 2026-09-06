package main

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

const deviceBrowserTimeout = 3 * time.Second

var errDeviceBrowserUnavailable = errors.New("device authorization browser is unavailable")

func openDeviceBrowser(ctx context.Context, raw string) error {
	ctx, cancel := context.WithTimeout(ctx, deviceBrowserTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validDeviceBrowserURL(raw) {
		return errDeviceBrowserUnavailable
	}
	command, err := deviceBrowserCommand(ctx, raw)
	if err != nil || command == nil {
		return err
	}
	return runDeviceBrowserCommand(ctx, command)
}

func validDeviceBrowserURL(raw string) bool {
	if raw == "" || len(raw) > 4096 || strings.Contains(raw, "\\") {
		return false
	}
	for _, character := range raw {
		if character <= 32 || character >= 127 {
			return false
		}
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil &&
		parsed.Opaque == "" && parsed.Fragment == "" && parsed.String() == raw
}

func runDeviceBrowserCommand(ctx context.Context, command *exec.Cmd) error {
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errDeviceBrowserUnavailable
	}
	return nil
}
