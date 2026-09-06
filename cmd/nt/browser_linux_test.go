package main

import (
	"context"
	"reflect"
	"testing"
)

func TestDeviceBrowserSkipsHeadlessLinux(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	command, err := deviceBrowserCommand(context.Background(), "https://auth.test/device")
	if err != nil || command != nil {
		t.Fatalf("headless Linux attempted a browser launch: %v", err)
	}
}

func TestDeviceBrowserUsesLinuxDesktopLauncherWithoutShell(t *testing.T) {
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	target := "https://auth.test/device?user_code=ABCD-EFGH&ui_locales=en"
	command, err := deviceBrowserCommand(context.Background(), target)
	if err != nil || command == nil || !reflect.DeepEqual(command.Args, []string{"xdg-open", target}) || command.Cancel == nil {
		t.Fatalf("desktop browser launcher changed the URL argument or omitted cancellation: %v", err)
	}
}
