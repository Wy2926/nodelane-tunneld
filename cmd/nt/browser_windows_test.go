package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestDeviceBrowserUsesHiddenWindowsSystemLauncherWithoutShell(t *testing.T) {
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), deviceBrowserTimeout)
	defer cancel()
	target := "https://auth.test/device?user_code=ABCD-EFGH&ui_locales=en"
	command, err := deviceBrowserCommand(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(systemDirectory, "rundll32.exe"), filepath.Join(systemDirectory, "url.dll") + ",FileProtocolHandler", target}
	if !reflect.DeepEqual(command.Args, want) || command.Path != want[0] || command.SysProcAttr == nil || !command.SysProcAttr.HideWindow || command.Cancel == nil {
		t.Fatal("browser opener did not use the cancellable hidden system launcher and separate URL argument")
	}
}

func TestDeviceBrowserTimeoutWaitsForWindowsLauncherExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDeviceBrowserLauncherProcess$")
	command.Env = append(os.Environ(), "NODELANE_BROWSER_LAUNCHER_HELPER=1")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	started := time.Now()
	if err := runDeviceBrowserCommand(ctx, command); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting launcher did not time out: %v", err)
	}
	if command.ProcessState == nil || time.Since(started) > 2*time.Second {
		t.Fatal("browser timeout did not promptly reap the launcher process")
	}
}

func TestDeviceBrowserLauncherProcess(t *testing.T) {
	if os.Getenv("NODELANE_BROWSER_LAUNCHER_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	<-time.After(time.Minute)
}
