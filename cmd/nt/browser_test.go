package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDeviceBrowserRejectsUnsafeURLsWithoutLaunching(t *testing.T) {
	for _, raw := range []string{
		"", "file:///tmp/test", "javascript:alert(1)", "http://auth.test/device", "https://user:secret@auth.test/device",
		"https://auth.test/device\ncommand", `https://auth.test\device`, `https://auth.test/device"argument`,
		"https://auth.test/device#fragment", "https://auth.test/" + strings.Repeat("a", 4096),
	} {
		if err := openDeviceBrowser(context.Background(), raw); !errors.Is(err, errDeviceBrowserUnavailable) {
			t.Fatalf("unsafe browser URL was accepted: %v", err)
		}
	}
}

func TestDeviceBrowserAcceptsPublicDeviceCodeQuery(t *testing.T) {
	for _, raw := range []string{"https://auth.test/device", "https://auth.test/device?user_code=ABCD-EFGH&ui_locales=zh-CN"} {
		if !validDeviceBrowserURL(raw) {
			t.Fatal("valid device authorization URL rejected")
		}
	}
}

func TestDeviceBrowserCanceledContextNeverLaunches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := openDeviceBrowser(ctx, "https://auth.test/device"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled browser launch returned %v", err)
	}
}
