package main

import "testing"

func TestTailBufferKeepsBoundedSuffix(t *testing.T) {
	buffer := newTailBuffer(8)
	_, _ = buffer.Write([]byte("12345"))
	_, _ = buffer.Write([]byte("67890"))
	if got := buffer.String(); got != "34567890" {
		t.Fatalf("tail buffer = %q, want %q", got, "34567890")
	}
	_, _ = buffer.Write([]byte("abcdefghijk"))
	if got := buffer.String(); got != "defghijk" {
		t.Fatalf("tail buffer after large write = %q", got)
	}
}

func TestFormatBytes(t *testing.T) {
	for value, want := range map[uint64]string{0: "0 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 1024 * 1024: "1.0 MiB"} {
		if got := formatBytes(value); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", value, got, want)
		}
	}
}

func TestSafeConsoleFieldRemovesControlCharacters(t *testing.T) {
	if got := safeConsoleField("203.0.113.9\x1b[31m\n", 64); got != "203.0.113.9[31m" {
		t.Fatalf("safe console field = %q", got)
	}
}
